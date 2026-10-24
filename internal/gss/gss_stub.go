//go:build !darwin || !cgo

package gss

import (
	"context"

	E "github.com/sagernet/sing/common/exceptions"
)

type Initiator struct{}

func NewInitiator(ctx context.Context, username, password, host, service string) (*Initiator, error) {
	return nil, E.New("gss: Kerberos is not supported on this platform")
}

func (i *Initiator) Step(input []byte) ([]byte, bool, error) {
	return nil, false, E.New("gss: Kerberos is not supported on this platform")
}

func (i *Initiator) Unwrap(input []byte) ([]byte, error) {
	return nil, E.New("gss: Kerberos is not supported on this platform")
}

func (i *Initiator) Close() {}
