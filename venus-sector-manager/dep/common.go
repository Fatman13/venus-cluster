package dep

import (
	"github.com/dtynn/dix"

	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/pkg/logging"
)

var log = logging.New("dep")

const (
	ignoredInvoke dix.Invoke = iota // nolint:deadcode,varcheck
	StartPoSter
	StartMiner

	// InvokePopulate should always be the last Invoke
	InvokePopulate
)

const (
	ignoredSpiecial dix.Special = iota // nolint:deadcode,varcheck
	ConstructMarketAPIRelated
)

const (
	HttpEndpointPiecestore = "/piecestore/"
)
