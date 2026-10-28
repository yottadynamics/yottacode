package acp

import (
	"context"

	coderacp "github.com/coder/acp-go-sdk"
	"github.com/yottadynamics/yottacode/internal/cli"
)

type fakeOutbound struct{ id string }

func (*fakeOutbound) SessionUpdate(context.Context, coderacp.SessionNotification) error { return nil }
func (*fakeOutbound) RequestPermission(context.Context, coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	return coderacp.RequestPermissionResponse{}, nil
}

func testOptions() cli.ChatOptions { return cli.ChatOptions{} }
