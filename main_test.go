package main_test

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/perun-network/perun-credential-payment/app"
	"github.com/perun-network/perun-credential-payment/client"
	"github.com/perun-network/perun-credential-payment/client/channel"
	"github.com/perun-network/perun-credential-payment/test"
	"github.com/stretchr/testify/require"
	ethwallet "perun.network/go-perun/backend/ethereum/wallet"
)

func TestCredentialSwapHonest(t *testing.T) {
	runCredentialSwapTest(t, true)
}

func TestCredentialSwapDishonest(t *testing.T) {
	runCredentialSwapTest(t, false)
}

func runCredentialSwapTest(t *testing.T, honestHolder bool) {
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Setup test environment.
	env := test.Setup(t)
	env.LogAccountBalances()
	wg, errs := sync.WaitGroup{}, make(chan error)
	wg.Add(2)
	holder, issuer := env.Holder, env.Issuer

	doc := []byte("Perun/Bosch: SSI Credential Payment")
	balance := test.EthToWei(big.NewFloat(10))
	price := test.EthToWei(big.NewFloat(5))

	// Run credential holder.
	go func() {
		err := runCredentialHolder(
			ctx,
			holder,
			issuer.PerunAddress(),
			balance,
			doc,
			price,
			honestHolder,
		)
		if err != nil {
			errs <- fmt.Errorf("running credential holder: %w", err)
			return
		}

		wg.Done()
	}()

	// Run credential issuer.
	go func() {
		err := runCredentialIssuer(
			ctx,
			issuer,
			holder.PerunAddress(),
			doc,
			price,
		)
		if err != nil {
			errs <- fmt.Errorf("running credential issuer: %w", err)
			return
		}

		wg.Done()
	}()

	// Await result.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		done <- struct{}{}
	}()
	err := func() error {
		select {
		case <-done:
			return nil
		case err := <-errs:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}()
	require.NoError(err)

	env.LogAccountBalances()
}

func runCredentialHolder(
	ctx context.Context,
	holder *client.Client,
	issuer *ethwallet.Address,
	balance *big.Int,
	doc []byte,
	price *big.Int,
	honest bool,
) error {
	// Open channel.
	holder.Logf("Opening channel")
	ch, err := holder.OpenChannel(ctx, issuer, balance)
	if err != nil {
		return fmt.Errorf("opening channel: %w", err)
	}

	// Buy credential.
	{
		// Request credential.
		holder.Logf("Requesting credential")
		asyncCred, err := ch.RequestCredential(ctx, doc, price, issuer)
		if err != nil {
			return fmt.Errorf("requesting credential: %w", err)
		}

		// Wait for the transaction that issues the credential.
		resp, err := asyncCred.Await(ctx)
		if err != nil {
			return fmt.Errorf("awaiting credential: %w", err)
		}

		cred := app.Credential{
			Document:  doc,
			Signature: resp.Signature,
		}
		holder.Logf("Obtained credential: %v", cred.String())

		// The issuer is waiting for us to complete the transaction.
		// If we are honest, we accept. If we are dishonest, we reject.
		if honest {
			holder.Logf("Accepting credential update")
			err := resp.Accept(ctx)
			if err != nil {
				return fmt.Errorf("accepting transaction: %w", err)
			}
		} else {
			holder.Logf("Rejecting credential update")
			err := resp.Reject(ctx, "Won't pay!")
			if err != nil {
				return fmt.Errorf("rejecting transaction: %w", err)
			}

			// We wait for the dispute to be resolved.
			err = ch.WaitConcludadable(ctx)
			if err != nil {
				return fmt.Errorf("waiting for dispute resolution: %w", err)
			}
		}
	}

	// Close channel.
	holder.Logf("Closing channel")
	err = ch.Close(ctx)
	if err != nil {
		return fmt.Errorf("closing channel: %w", err)
	}

	return nil
}

func runCredentialIssuer(
	ctx context.Context,
	issuer *client.Client,
	holder *ethwallet.Address,
	doc []byte,
	price *big.Int,
) error {
	// Await channel request and accept.
	ch, err := func() (*channel.Channel, error) {
		// Read next channel request.
		issuer.Logf("Awaiting channel request")
		req, err := issuer.NextChannelRequest(ctx)
		if err != nil {
			return nil, fmt.Errorf("awaiting next channel request: %w", err)
		}

		// Check peer.
		if !req.Peer().Equals(holder) {
			return nil, fmt.Errorf("wrong peer: expected %v, got %v", holder, req.Peer())
		}

		// Accept.
		issuer.Logf("Accepting channel request")
		ch, err := req.Accept(ctx)
		if err != nil {
			return nil, fmt.Errorf("accepting channel request: %w", err)
		}

		return ch, nil
	}()
	if err != nil {
		return fmt.Errorf("accepting channel: %w", err)
	}

	// Issue credential.
	err = func() error {
		// Read next credential request.
		issuer.Logf("Awaiting credential request")
		req, err := ch.NextCredentialRequest(ctx)
		if err != nil {
			return fmt.Errorf("awaiting next credential request: %w", err)
		}

		// Check document and price.
		if err := req.CheckDoc(doc); err != nil {
			return fmt.Errorf("checking document: %w", err)
		} else if err := req.CheckPrice(price); err != nil {
			return fmt.Errorf("checking price: %w", err)
		}

		// Issue credential.
		issuer.Logf("Issueing credential")
		err = req.IssueCredential(ctx, issuer.Account())
		if err != nil {
			return fmt.Errorf("issueing credential: %w", err)
		}

		return nil
	}()
	if err != nil {
		return fmt.Errorf("issueing credential: %w", err)
	}

	// Wait until channel is concludable.
	err = ch.WaitConcludadable(ctx)
	if err != nil {
		return fmt.Errorf("waiting for channel finalization: %w", err)
	}

	// Close channel.
	issuer.Logf("Closing channel")
	err = ch.Close(ctx)
	if err != nil {
		return fmt.Errorf("closing channel: %w", err)
	}

	return nil
}
