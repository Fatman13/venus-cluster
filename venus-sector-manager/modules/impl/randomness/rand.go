package randomness

import (
	"bytes"
	"context"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/crypto"

	"github.com/filecoin-project/venus/venus-shared/types"

	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/api"
	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/pkg/chain"
)

var _ api.RandomnessAPI = (*Randomness)(nil)

func New(capi chain.API) (api.RandomnessAPI, error) {
	return &Randomness{
		api: capi,
	}, nil
}

type Randomness struct {
	api chain.API
}

func (r *Randomness) getRandomnessEntropy(mid abi.ActorID) ([]byte, error) {
	maddr, err := address.NewIDAddress(uint64(mid))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := maddr.MarshalCBOR(&buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (r *Randomness) GetTicket(ctx context.Context, tsk types.TipSetKey, epoch abi.ChainEpoch, mid abi.ActorID) (api.Ticket, error) {
	entropy, err := r.getRandomnessEntropy(mid)
	if err != nil {
		return api.Ticket{}, err
	}

	if tsk == types.EmptyTSK {
		ts, err := r.api.ChainHead(ctx)
		if err != nil {
			return api.Ticket{}, err
		}

		tsk = ts.Key()
	}

	rand, err := r.api.ChainGetRandomnessFromTickets(ctx, tsk, crypto.DomainSeparationTag_SealRandomness, epoch, entropy)
	if err != nil {
		return api.Ticket{}, err
	}

	return api.Ticket{
		Ticket: rand,
		Epoch:  epoch,
	}, nil
}

func (r *Randomness) GetSeed(ctx context.Context, tsk types.TipSetKey, epoch abi.ChainEpoch, mid abi.ActorID) (api.Seed, error) {
	entropy, err := r.getRandomnessEntropy(mid)
	if err != nil {
		return api.Seed{}, err
	}

	if tsk == types.EmptyTSK {
		ts, err := r.api.ChainHead(ctx)
		if err != nil {
			return api.Seed{}, err
		}

		tsk = ts.Key()
	}

	rand, err := r.api.ChainGetRandomnessFromBeacon(ctx, tsk, crypto.DomainSeparationTag_InteractiveSealChallengeSeed, epoch, entropy)
	if err != nil {
		return api.Seed{}, err
	}

	return api.Seed{
		Seed:  rand,
		Epoch: epoch,
	}, nil
}

func (r *Randomness) GetWindowPoStChanlleengeRand(ctx context.Context, tsk types.TipSetKey, epoch abi.ChainEpoch, mid abi.ActorID) (api.WindowPoStRandomness, error) {
	entropy, err := r.getRandomnessEntropy(mid)
	if err != nil {
		return api.WindowPoStRandomness{}, err
	}

	if tsk == types.EmptyTSK {
		ts, err := r.api.ChainHead(ctx)
		if err != nil {
			return api.WindowPoStRandomness{}, err
		}

		tsk = ts.Key()
	}

	rand, err := r.api.ChainGetRandomnessFromBeacon(ctx, tsk, crypto.DomainSeparationTag_WindowedPoStChallengeSeed, epoch, entropy)
	if err != nil {
		return api.WindowPoStRandomness{}, err
	}

	return api.WindowPoStRandomness{
		Rand:  rand,
		Epoch: epoch,
	}, nil
}

func (r *Randomness) GetWindowPoStCommitRand(ctx context.Context, tsk types.TipSetKey, epoch abi.ChainEpoch) (api.WindowPoStRandomness, error) {
	if tsk == types.EmptyTSK {
		ts, err := r.api.ChainHead(ctx)
		if err != nil {
			return api.WindowPoStRandomness{}, err
		}

		tsk = ts.Key()
	}

	rand, err := r.api.ChainGetRandomnessFromTickets(ctx, tsk, crypto.DomainSeparationTag_PoStChainCommit, epoch, nil)
	if err != nil {
		return api.WindowPoStRandomness{}, err
	}

	return api.WindowPoStRandomness{
		Rand:  rand,
		Epoch: epoch,
	}, nil
}
