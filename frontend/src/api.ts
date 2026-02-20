import { createConnectTransport } from "@connectrpc/connect-web";
import { createPromiseClient } from "@connectrpc/connect";
import { WalletService } from "./gen/wallet/v1/wallet_connect";

const transport = createConnectTransport({
  baseUrl: import.meta.env.VITE_API_BASE_URL,
});

export const client = createPromiseClient(WalletService, transport);
