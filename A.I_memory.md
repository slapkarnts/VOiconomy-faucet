# VirtualbabesTT AI Memory

## Project Overview
A high-fidelity Triple Triad battle arena and faucet built on the Voi network, utilizing a Go WASM engine and a secure Go backend switchboard.

## Infrastructure Context
- **Voi Node**: Local Docker container managed by `func` on port `3536`.
- **Vault Address**: `2A3NWJMYQ7AWJ5KIMYJKWEZS37FWND3PEXT3XPS6QONL6MDRJO257C7WEI`.
- **Backend**: Authoritative referee for moves, winner verification, and multi-reward atomic payouts.
- **Frontend**: Embedded via Carrd.co, static assets hosted on GitHub Pages.

## Security Architecture
- **Replay Protection**: Cryptographic nonces generated per session.
- **Identity Verification**: "Reverse Sign" proofs via WalletConnect/SignClient.
- **Vault Safety**: Mnemonic and Admin Keys stored strictly in environment variables.
- **Network Security**: WebSocket origin filtering via `ALLOWED_ORIGINS`.

## Build Commands
- **WASM**: `$env:GOOS="js"; $env:GOARCH="wasm"; go build -o main.wasm main.go`
- **Server**: `go build -o server.exe server.go`

## Known Constraints
- WASM glue code must support Go 1.23+ (`resetMemoryDataView`).
- Card power levels are derived from ARC-72 metadata and price heuristics.