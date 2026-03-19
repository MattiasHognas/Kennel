package acp

import "context"

type Client interface {
Prompt(ctx context.Context, msg string) (string, error)
}

type FakeClient struct {
Response string
}

func (f *FakeClient) Prompt(ctx context.Context, msg string) (string, error) {
return f.Response, nil
}

