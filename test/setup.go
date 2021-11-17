package test

import (
	"context"
	"crypto/ecdsa"
	"log"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/perun-network/verifiable-credential-payment/client"
	"github.com/perun-network/verifiable-credential-payment/client/perun"
	"github.com/perun-network/verifiable-credential-payment/pkg/ganache"
	"github.com/stretchr/testify/require"
	"perun.network/go-perun/backend/ethereum/wallet"
)

const (
	ganacheHost = "127.0.0.1"
	ganachePort = 8545
	chainID     = 1337
	blockTime   = 1 * time.Second
	txFinality  = 1

	disputeDuration = 3 * time.Second

	// Client hosts.
	holderHost = "127.0.0.1:8546"
	issuerHost = "127.0.0.1:8547"
)

// Accounts and initial funding.
var accountFunding = []ganache.KeyWithBalance{
	{PrivateKey: "0x50b4713b4ba55b6fbcb826ae04e66c03a12fc62886a90ca57ab541959337e897", BalanceEth: 10},  // Contract Deployer
	{PrivateKey: "0x1af2e950272dd403de7a5760d41c6e44d92b6d02797e51810795ff03cc2cda4f", BalanceEth: 100}, // Holder
	{PrivateKey: "0xf63d7d8e930bccd74e93cf5662fde2c28fd8be95edb70c73f1bdd863d07f412e", BalanceEth: 200}, // Issuer
}

type Environment struct {
	Holder, Issuer *client.Client
}

func (e *Environment) LogAccountBalances() {
	LogAccountBalance(e.Holder, e.Issuer)
}

func Setup(t *testing.T, honestHolder bool) *Environment {
	t.Helper()
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Ganache config
	ganacheCfg := makeGanacheConfig(accountFunding)

	// Start ganache blockchain with prefunded accounts
	log.Print("Starting local blockchain...")
	ganache, err := ganache.StartGanacheWithPrefundedAccounts(ganacheCfg)
	require.NoError(err, "starting ganache")
	t.Cleanup(func() {
		err := ganache.Shutdown()
		if err != nil {
			log.Print("shutting down ganache:", err)
		}
	})

	// Deploy contracts
	log.Print("Deploying contracts...")
	nodeURL := ganacheCfg.NodeURL()
	deploymentKey := ganache.Accounts[0].PrivateKey
	contracts, err := deployContracts(ctx, nodeURL, ganacheCfg.ChainID, deploymentKey)
	require.NoError(err, "deploying contracts")

	log.Print("Setting up clients...")
	// Setup holder.
	holderConfig := newClientConfig(
		nodeURL, contracts,
		ganache.Accounts[1].PrivateKey, holderHost,
		ganache.Accounts[2].Address(), issuerHost,
		honestHolder,
	)
	holder, err := client.StartClient(ctx, holderConfig)
	require.NoError(err, "Holder setup")

	// Setup issuer.
	issuerConfig := newClientConfig(
		nodeURL, contracts,
		ganache.Accounts[2].PrivateKey, issuerHost,
		ganache.Accounts[1].Address(), holderHost,
		true,
	)
	issuer, err := client.StartClient(ctx, issuerConfig)
	require.NoError(err, "Issuer setup")
	log.Print("Setup done.")

	return &Environment{Holder: holder, Issuer: issuer}
}

func makeGanacheConfig(funding []ganache.KeyWithBalance) ganache.GanacheConfig {
	ganacheCmd := os.Getenv("GANACHE_CMD")
	if len(ganacheCmd) == 0 {
		ganacheCmd = "ganache-cli"
	}
	return ganache.GanacheConfig{
		Cmd:         ganacheCmd,
		Host:        ganacheHost,
		Port:        ganachePort,
		BlockTime:   blockTime,
		Funding:     funding,
		StartupTime: 3 * time.Second,
		ChainID:     big.NewInt(chainID),
	}
}

func newClientConfig(
	nodeURL string,
	contracts ContractAddresses,
	privateKey *ecdsa.PrivateKey,
	host string,
	peerAddress common.Address,
	peerHost string,
	honest bool,
) client.ClientConfig {
	return client.ClientConfig{
		ClientConfig: perun.ClientConfig{
			PrivateKey:    privateKey,
			Host:          host,
			ETHNodeURL:    nodeURL,
			Adjudicator:   contracts.Adjudicator,
			AssetHolder:   contracts.AssetHolder,
			DialerTimeout: 1 * time.Second,
			Peers: []perun.Peer{
				{
					Peer:    wallet.AsWalletAddr(peerAddress),
					Address: peerHost,
				},
			},
			TxFinality: txFinality,
			ChainID:    big.NewInt(chainID),
		},
		ChallengeDuration: disputeDuration,
		AppAddress:        contracts.App,
		Honest:            honest,
	}
}