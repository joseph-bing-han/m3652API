package main

import (
	"time"

	"github.com/joseph/m3652api/internal/oauthstate"
)

// 说明：该文件仅保留为兼容层，核心实现已迁移到 internal/oauthstate，
// 从而保证 `go run cmd/m3652api/main.go`（只编译单文件）也能工作。

type oauthPending = oauthstate.Pending
type oauthStateStore = oauthstate.Store

func newOAuthStateStore(ttl time.Duration) *oauthStateStore { return oauthstate.NewStore(ttl) }

func newPKCE() (verifier string, challenge string, err error) { return oauthstate.NewPKCE() }
