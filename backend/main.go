package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	_ "github.com/lib/pq"

	walletv1 "connect-demo/backend/gen/wallet/v1"
	"connect-demo/backend/gen/wallet/v1/walletv1connect"
)

var db *sql.DB

type WalletServer struct{}

func initDB() error {
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "postgres"
	}
	dbPort := os.Getenv("DB_PORT")
	if dbPort == "" {
		dbPort = "5432"
	}
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "postgres"
	}
	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		dbPassword = "password123"
	}
	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "wallet_db"
	}

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		dbHost, dbPort, dbUser, dbPassword, dbName)

	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	log.Println("✅ Connected to PostgreSQL")
	return nil
}

// GetBalance returns current wallet balance
func (s *WalletServer) GetBalance(ctx context.Context, req *connect.Request[walletv1.GetBalanceRequest]) (*connect.Response[walletv1.GetBalanceResponse], error) {
	if req.Msg.UserId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id is required"))
	}

	var balance int64
	var currency string
	var lastUpdated time.Time

	err := db.QueryRowContext(ctx,
		"SELECT balance, currency, updated_at FROM wallets WHERE user_id = $1",
		req.Msg.UserId).
		Scan(&balance, &currency, &lastUpdated)

	if err == sql.ErrNoRows {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("wallet not found"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get balance: %w", err))
	}

	return connect.NewResponse(&walletv1.GetBalanceResponse{
		UserId:      req.Msg.UserId,
		Balance:     float64(balance) / 100, // Convert cents to dollars
		Currency:    currency,
		LastUpdated: lastUpdated.Unix(),
	}), nil
}

// Deposit adds funds to wallet
func (s *WalletServer) Deposit(ctx context.Context, req *connect.Request[walletv1.DepositRequest]) (*connect.Response[walletv1.DepositResponse], error) {
	if req.Msg.UserId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id is required"))
	}
	if req.Msg.Amount <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("amount must be greater than 0"))
	}

	// Convert amount to cents (integer)
	amountCents := int64(req.Msg.Amount * 100)

	// Start transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to begin transaction: %w", err))
	}
	defer tx.Rollback()

	// Ensure user exists
	var exists bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE user_id = $1)", req.Msg.UserId).Scan(&exists)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check user: %w", err))
	}
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	}

	// Ensure wallet exists, create if not
	var walletExists bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM wallets WHERE user_id = $1)", req.Msg.UserId).Scan(&walletExists)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check wallet: %w", err))
	}

	if !walletExists {
		_, err = tx.ExecContext(ctx,
			"INSERT INTO wallets (user_id, balance, currency, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
			req.Msg.UserId, amountCents, "USD", time.Now(), time.Now())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create wallet: %w", err))
		}
	} else {
		_, err = tx.ExecContext(ctx,
			"UPDATE wallets SET balance = balance + $1, updated_at = $2 WHERE user_id = $3",
			amountCents, time.Now(), req.Msg.UserId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update wallet: %w", err))
		}
	}

	// Get new balance
	var newBalance int64
	err = tx.QueryRowContext(ctx, "SELECT balance FROM wallets WHERE user_id = $1", req.Msg.UserId).Scan(&newBalance)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get new balance: %w", err))
	}

	// Create transaction record
	transactionID := uuid.New().String()
	now := time.Now()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO transactions (transaction_id, user_id, type, amount, description, balance_after, status, created_at)
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		transactionID, req.Msg.UserId, "deposit", amountCents, req.Msg.Description, newBalance, "completed", now)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create transaction: %w", err))
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to commit transaction: %w", err))
	}

	return connect.NewResponse(&walletv1.DepositResponse{
		TransactionId: transactionID,
		NewBalance:    float64(newBalance) / 100,
		Timestamp:     now.Unix(),
		Success:       true,
	}), nil
}

// MakePayment transfers funds between wallets
func (s *WalletServer) MakePayment(ctx context.Context, req *connect.Request[walletv1.MakePaymentRequest]) (*connect.Response[walletv1.MakePaymentResponse], error) {
	if req.Msg.FromUserId == "" || req.Msg.ToUserId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("from_user_id and to_user_id are required"))
	}
	if req.Msg.Amount <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("amount must be greater than 0"))
	}
	if req.Msg.FromUserId == req.Msg.ToUserId {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cannot transfer to same user"))
	}

	amountCents := int64(req.Msg.Amount * 100)

	// Start transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to begin transaction: %w", err))
	}
	defer tx.Rollback()

	// Check sender balance
	var senderBalance int64
	err = tx.QueryRowContext(ctx, "SELECT balance FROM wallets WHERE user_id = $1", req.Msg.FromUserId).Scan(&senderBalance)
	if err == sql.ErrNoRows {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("sender wallet not found"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check balance: %w", err))
	}

	// Check sufficient balance
	if senderBalance < amountCents {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("insufficient balance"))
	}

	// Check recipient exists
	var recipientExists bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE user_id = $1)", req.Msg.ToUserId).Scan(&recipientExists)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check recipient: %w", err))
	}
	if !recipientExists {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("recipient not found"))
	}

	// Ensure recipient wallet exists
	var recipientWalletExists bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM wallets WHERE user_id = $1)", req.Msg.ToUserId).Scan(&recipientWalletExists)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check recipient wallet: %w", err))
	}

	if !recipientWalletExists {
		_, err = tx.ExecContext(ctx,
			"INSERT INTO wallets (user_id, balance, currency, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
			req.Msg.ToUserId, 0, "USD", time.Now(), time.Now())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create recipient wallet: %w", err))
		}
	}

	// Deduct from sender
	_, err = tx.ExecContext(ctx,
		"UPDATE wallets SET balance = balance - $1, updated_at = $2 WHERE user_id = $3",
		amountCents, time.Now(), req.Msg.FromUserId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to deduct from sender: %w", err))
	}

	// Add to recipient
	_, err = tx.ExecContext(ctx,
		"UPDATE wallets SET balance = balance + $1, updated_at = $2 WHERE user_id = $3",
		amountCents, time.Now(), req.Msg.ToUserId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to add to recipient: %w", err))
	}

	// Get sender's new balance
	var newBalance int64
	err = tx.QueryRowContext(ctx, "SELECT balance FROM wallets WHERE user_id = $1", req.Msg.FromUserId).Scan(&newBalance)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get new balance: %w", err))
	}

	// Create transaction record
	transactionID := uuid.New().String()
	now := time.Now()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO transactions (transaction_id, user_id, type, amount, description, balance_after, status, from_user_id, to_user_id, created_at)
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		transactionID, req.Msg.FromUserId, "payment", amountCents, req.Msg.Description, newBalance, "completed", req.Msg.FromUserId, req.Msg.ToUserId, now)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create transaction: %w", err))
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to commit transaction: %w", err))
	}

	return connect.NewResponse(&walletv1.MakePaymentResponse{
		TransactionId:    transactionID,
		RemainingBalance: float64(newBalance) / 100,
		Timestamp:        now.Unix(),
		Success:          true,
	}), nil
}

// GetTransactionHistory returns user's transaction history
func (s *WalletServer) GetTransactionHistory(ctx context.Context, req *connect.Request[walletv1.GetTransactionHistoryRequest]) (*connect.Response[walletv1.GetTransactionHistoryResponse], error) {
	if req.Msg.UserId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id is required"))
	}

	limit := req.Msg.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	offset := req.Msg.Offset
	if offset < 0 {
		offset = 0
	}

	// Get total count
	var total int32
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions WHERE user_id = $1", req.Msg.UserId).Scan(&total)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to count transactions: %w", err))
	}

	// Get transactions
	rows, err := db.QueryContext(ctx,
		`SELECT id, transaction_id, user_id, type, amount, description, balance_after, status, from_user_id, to_user_id, created_at
         FROM transactions WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		req.Msg.UserId, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to query transactions: %w", err))
	}
	defer rows.Close()

	var transactions []*walletv1.Transaction
	for rows.Next() {
		var id int
		var transactionID string
		var userID string
		var txType string
		var amount int64
		var description string
		var balanceAfter int64
		var status string
		var fromUserID sql.NullString
		var toUserID sql.NullString
		var createdAt time.Time

		if err := rows.Scan(&id, &transactionID, &userID, &txType, &amount, &description, &balanceAfter, &status, &fromUserID, &toUserID, &createdAt); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to scan transaction: %w", err))
		}

		tx := &walletv1.Transaction{
			Id:           transactionID,
			UserId:       userID,
			Type:         txType,
			Amount:       float64(amount) / 100,
			Description:  description,
			BalanceAfter: float64(balanceAfter) / 100,
			CreatedAt:    createdAt.Unix(),
			Status:       status,
		}
		if fromUserID.Valid {
			tx.FromUserId = fromUserID.String
		}
		if toUserID.Valid {
			tx.ToUserId = toUserID.String
		}

		transactions = append(transactions, tx)
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error iterating transactions: %w", err))
	}

	return connect.NewResponse(&walletv1.GetTransactionHistoryResponse{
		Transactions: transactions,
		Total:        total,
	}), nil
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type,Connect-Protocol-Version,Connect-Timeout-Ms,X-User-Agent")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	if err := initDB(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	path, handler := walletv1connect.NewWalletServiceHandler(&WalletServer{})

	mux.Handle(path, handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Println("✨ Wallet Backend running on :8080")
	log.Println("📡 Connect RPC endpoint:", path)
	log.Println("🗄️  Database: PostgreSQL (wallet_db)")
	log.Println("❤️  Health endpoint: /health")

	if err := http.ListenAndServe(":8080", withCORS(mux)); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
