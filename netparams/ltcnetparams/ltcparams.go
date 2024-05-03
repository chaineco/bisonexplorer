// Copyright (c) 2013-2015 The btcsuite developers
// Copyright (c) 2016-2017 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package ltcnetparams

import "github.com/ltcsuite/ltcd/chaincfg"

// Params is used to group parameters for various networks such as the main
// network and test networks.
type Params struct {
	*chaincfg.Params
	JSONRPCClientPort string
	JSONRPCServerPort string
	GRPCServerPort    string
}

// MainNetParams contains parameters specific running dcrwallet and
// dcrd on the main network (wire.MainNet).
var MainNetParams = Params{
	Params:            &chaincfg.MainNetParams,
	JSONRPCClientPort: "9334",
	JSONRPCServerPort: "9335",
	GRPCServerPort:    "9336",
}

// TestNet3Params contains parameters specific running dcrwallet and
// dcrd on the test network (version 3) (wire.TestNet3).
var TestNet3Params = Params{
	Params:            &chaincfg.TestNet4Params,
	JSONRPCClientPort: "19334",
	JSONRPCServerPort: "19335",
	GRPCServerPort:    "19336",
}

// SimNetParams contains parameters specific to the simulation test network
// (wire.SimNet).
var SimNetParams = Params{
	Params:            &chaincfg.SimNetParams,
	JSONRPCClientPort: "18556",
	JSONRPCServerPort: "18557",
	GRPCServerPort:    "18558",
}
