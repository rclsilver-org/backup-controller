package outputs

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/rclsilver-org/backup-controller/agents/common"
)

const (
	ICINGA_OUTPUT = "icinga"

	BC_OUTPUT_ICINGA_API_URL = "BC_OUTPUT_ICINGA_API_URL"
	BC_OUTPUT_ICINGA_USER    = "BC_OUTPUT_ICINGA_USER"
	BC_OUTPUT_ICINGA_PASS    = "BC_OUTPUT_ICINGA_PASS"
	BC_OUTPUT_ICINGA_SERVICE = "BC_OUTPUT_ICINGA_SERVICE"
	BC_OUTPUT_ICINGA_HOST    = "BC_OUTPUT_ICINGA_HOST"
)

func init() {
	register(ICINGA_OUTPUT, &icingaOutput{})
}

type icingaOutput struct {
	apiURL      string
	apiUsername string
	apiPassword string

	host    string
	service string
}

func (o *icingaOutput) Name() string {
	return ICINGA_OUTPUT
}

func (o *icingaOutput) Init() error {
	if err := common.RequiredEnvVar(BC_OUTPUT_ICINGA_API_URL, BC_OUTPUT_ICINGA_USER, BC_OUTPUT_ICINGA_PASS, BC_OUTPUT_ICINGA_SERVICE, BC_OUTPUT_ICINGA_HOST); err != nil {
		return err
	}

	o.apiURL = common.GetEnv(BC_OUTPUT_ICINGA_API_URL, "")
	o.apiUsername = common.GetEnv(BC_OUTPUT_ICINGA_USER, "")
	o.apiPassword = common.GetEnv(BC_OUTPUT_ICINGA_PASS, "")

	o.host = common.GetEnv(BC_OUTPUT_ICINGA_HOST, "")
	o.service = common.GetEnv(BC_OUTPUT_ICINGA_SERVICE, "")

	slog.Debug("icinga output initialized",
		"api_url", o.apiURL,
		"host", o.host,
		"service", o.service,
		"username", o.apiUsername)

	return nil
}

func (o *icingaOutput) SetSuccess(ctx context.Context, msg string, data map[string]any) error {
	slog.InfoContext(ctx, "sending SUCCESS status to Icinga", "host", o.host, "service", o.service, "message", msg)
	return o.send(ctx, 0, msg, data)
}

func (o *icingaOutput) SetWarning(ctx context.Context, err error) error {
	slog.InfoContext(ctx, "sending WARNING status to Icinga", "host", o.host, "service", o.service, "error", err.Error())
	return o.send(ctx, 1, err.Error(), nil)
}

func (o *icingaOutput) SetError(ctx context.Context, err error) error {
	slog.InfoContext(ctx, "sending ERROR status to Icinga", "host", o.host, "service", o.service, "error", err.Error())
	return o.send(ctx, 2, err.Error(), nil)
}

func (o *icingaOutput) SetUnknown(ctx context.Context, err error) error {
	slog.InfoContext(ctx, "sending UNKNOWN status to Icinga", "host", o.host, "service", o.service, "error", err.Error())
	return o.send(ctx, 3, err.Error(), nil)
}

type status struct {
	Type            string   `json:"type"`
	Filter          string   `json:"filter"`
	ExitStatus      int      `json:"exit_status"`
	PluginOutput    string   `json:"plugin_output"`
	PerformanceData []string `json:"performance_data,omitempty"`
	Pretty          bool     `json:"pretty"`
}

func (o *icingaOutput) send(ctx context.Context, exitStatus int, output string, performanceData map[string]any) error {
	status := status{
		Type:         "Service",
		Filter:       fmt.Sprintf("host.name==%q && service.name==%q", o.host, o.service),
		ExitStatus:   exitStatus,
		PluginOutput: output,
		Pretty:       true,
	}
	for k, v := range performanceData {
		status.PerformanceData = append(status.PerformanceData, fmt.Sprintf("%s=%v", k, v))
	}

	slog.DebugContext(ctx, "preparing to send status to icinga",
		"host", o.host,
		"service", o.service,
		"exit_status", exitStatus,
		"output", output,
		"performance_data", performanceData)

	payload, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("unable to marshal the status: %w", err)
	}

	slog.DebugContext(ctx, "status payload marshaled", "payload", string(payload))

	url := fmt.Sprintf("%s/v1/actions/process-check-result", o.apiURL)
	slog.DebugContext(ctx, "sending request to icinga", "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("unable to build the request: %w", err)
	}

	req.Header.Add("accept", "application/json")
	req.SetBasicAuth(o.apiUsername, o.apiPassword)

	client := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	res, err := client.Do(req)
	if err != nil {
		slog.DebugContext(ctx, "failed to send request to icinga", "error", err)
		return fmt.Errorf("unable to send the status: %w", err)
	}
	defer func() {
		if closeErr := res.Body.Close(); closeErr != nil {
			err = fmt.Errorf("failed to close response body: %w", closeErr)
		}
	}()

	slog.DebugContext(ctx, "received response from icinga", "status_code", res.StatusCode)

	if res.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			slog.DebugContext(ctx, "failed to read response body", "error", readErr)
			return fmt.Errorf("unexpected status code: %d (failed to read response body: %w)", res.StatusCode, readErr)
		}
		slog.DebugContext(ctx, "icinga returned error", "status_code", res.StatusCode, "response", string(bodyBytes))
		return fmt.Errorf("unexpected status code: %d, response: %s", res.StatusCode, string(bodyBytes))
	}

	bodyBytes, readErr := io.ReadAll(res.Body)
	if readErr == nil {
		slog.InfoContext(ctx, "status successfully sent to Icinga", "host", o.host, "service", o.service, "exit_status", exitStatus)
		slog.DebugContext(ctx, "icinga API response", "response", string(bodyBytes))
	} else {
		slog.InfoContext(ctx, "status successfully sent to Icinga", "host", o.host, "service", o.service, "exit_status", exitStatus)
		slog.DebugContext(ctx, "failed to read response body", "error", readErr)
	}

	return err
}
