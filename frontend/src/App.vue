<script setup lang="ts">
import { ref, onMounted } from "vue";
import { client } from "./api";

const userId = "user1";

const balance = ref<number>(0);
const depositAmount = ref<number>(0);
const paymentAmount = ref<number>(0);
const toUserId = ref<string>("");

const transactions = ref<any[]>([]);
const loading = ref<boolean>(false);
const error = ref<string>("");

// Load balance
const loadBalance = async () => {
  try {
    const res = await client.getBalance({ userId });
    balance.value = res.balance; // cents
  } catch (err: any) {
    error.value = err?.message ?? String(err);
  }
};

// Load transactions
const loadTransactions = async () => {
  try {
    const res = await client.getTransactionHistory({ userId });
    transactions.value = res.transactions || [];
  } catch (err: any) {
    error.value = err?.message ?? String(err);
  }
};

// Deposit
const deposit = async () => {
  if (!depositAmount.value) return;

  loading.value = true;
  error.value = "";

  try {
    await client.deposit({
      userId,
      amount: depositAmount.value * 100, // convert to cents
    });

    depositAmount.value = 0;
    await loadBalance();
    await loadTransactions();
  } catch (err: any) {
    error.value = err?.message ?? String(err);
  } finally {
    loading.value = false;
  }
};

// Make Payment
const makePayment = async () => {
  if (!paymentAmount.value || !toUserId.value) return;

  loading.value = true;
  error.value = "";

  console.log("Sending payment:", {
  fromUserId: userId,
  toUserId: toUserId.value,
  amount: paymentAmount.value * 100,
});

  try {
    await client.makePayment({
      fromUserId: userId,
      toUserId: toUserId.value,
      amount: paymentAmount.value * 100, // cents
    });

    paymentAmount.value = 0;
    toUserId.value = "";

    await loadBalance();
    await loadTransactions();
  } catch (err: any) {
    error.value = err?.message ?? String(err);
  } finally {
    loading.value = false;
  }
};

onMounted(async () => {
  await loadBalance();
  await loadTransactions();
});
</script>

<template>
  <div style="max-width: 700px; margin: auto; font-family: sans-serif;">
    <h1>💰 Wallet Dashboard</h1>

    <div v-if="error" style="color: red; margin-bottom: 10px;">
      {{ error }}
    </div>

    <h2>
      Balance: ${{ (balance / 100).toFixed(2) }}
    </h2>

    <hr />

    <h3>Deposit</h3>
    <input
      type="number"
      v-model.number="depositAmount"
      placeholder="Amount (USD)"
    />
    <button @click="deposit" :disabled="loading">Deposit</button>

    <hr />

    <h3>Make Payment</h3>
    <input
      type="text"
      v-model="toUserId"
      placeholder="Recipient User ID"
    />
    <input
      type="number"
      v-model.number="paymentAmount"
      placeholder="Amount (USD)"
    />
    <button @click="makePayment" :disabled="loading">Send</button>

    <hr />

    <h3>Transactions</h3>

    <div v-if="transactions.length === 0">
      No transactions yet.
    </div>

    <div
      v-for="tx in transactions"
      :key="tx.id"
      style="border: 1px solid #ddd; padding: 10px; margin-bottom: 5px;"
    >
      <strong>{{ tx.type }}</strong> —
      ${{ (tx.amount / 100).toFixed(2) }}
      <div style="font-size: 12px; color: gray;">
        Balance After: ${{ (tx.balanceAfter / 100).toFixed(2) }}
      </div>
    </div>
  </div>
</template>