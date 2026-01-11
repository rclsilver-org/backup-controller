package outputs

import "context"

const VOID_OUTPUT = "void"

func init() {
	register(VOID_OUTPUT, &voidOutput{})
}

type voidOutput struct{}

func (o *voidOutput) Name() string {
	return VOID_OUTPUT
}

func (o *voidOutput) Init() error {
	return nil
}

func (o *voidOutput) SetSuccess(ctx context.Context, msg string, data map[string]any) error {
	return nil
}

func (o *voidOutput) SetWarning(ctx context.Context, err error) error {
	return nil
}

func (o *voidOutput) SetError(ctx context.Context, err error) error {
	return nil
}

func (o *voidOutput) SetUnknown(ctx context.Context, err error) error {
	return nil
}
