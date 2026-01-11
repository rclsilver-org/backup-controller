package outputs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/rclsilver-org/backup-controller/agents/common"
)

type output interface {
	Name() string
	Init() error
	SetSuccess(ctx context.Context, msg string, data map[string]any) error
	SetWarning(ctx context.Context, err error) error
	SetError(ctx context.Context, err error) error
	SetUnknown(ctx context.Context, err error) error
}

var (
	outputs    map[string]output
	outputsMut sync.Mutex

	outputModules []output
)

func Init(ctx context.Context) error {
	outputTypes := common.GetEnv("BC_OUTPUT_MODULE", "")

	for _, outputType := range strings.Split(outputTypes, ",") {
		outputType = strings.TrimSpace(outputType)
		if outputType == "" {
			continue
		}

		output, ok := outputs[outputType]
		if !ok {
			return fmt.Errorf("output module %q not found", outputType)
		}

		slog.Default().DebugContext(ctx, "initializing output module", "module", outputType)
		if err := output.Init(); err != nil {
			return fmt.Errorf("unable to initialize output module %q: %w", outputType, err)
		}

		outputModules = append(outputModules, output)
	}

	return nil
}

func SetSuccess(ctx context.Context, msg string, data map[string]any) {
	for _, m := range outputModules {
		if err := m.SetSuccess(ctx, msg, data); err != nil {
			slog.ErrorContext(ctx, "unable to send success state", "module", m.Name(), "error", err)
		}
	}
}

func SetUnknown(ctx context.Context, e error) {
	for _, m := range outputModules {
		if err := m.SetUnknown(ctx, e); err != nil {
			slog.ErrorContext(ctx, "unable to send unknown state", "module", m.Name(), "error", err)
		}
	}
}

func SetWarning(ctx context.Context, e error) {
	for _, m := range outputModules {
		if err := m.SetWarning(ctx, e); err != nil {
			slog.ErrorContext(ctx, "unable to send warning state", "module", m.Name(), "error", err)
		}
	}
}

func SetError(ctx context.Context, e error) {
	for _, m := range outputModules {
		if err := m.SetError(ctx, e); err != nil {
			slog.ErrorContext(ctx, "unable to send error state", "module", m.Name(), "error", err)
		}
	}
}

func register(name string, obj output) {
	outputsMut.Lock()
	defer outputsMut.Unlock()

	if _, ok := outputs[name]; ok {
		panic(fmt.Sprintf("output %q already registered", name))
	}

	if outputs == nil {
		outputs = make(map[string]output)
	}

	outputs[name] = obj
}
