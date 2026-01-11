package outputs

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
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

	return nil
}

func (o *icingaOutput) SetSuccess(ctx context.Context, msg string, data map[string]any) error {
	return o.send(ctx, 0, msg, data)
}

func (o *icingaOutput) SetWarning(ctx context.Context, err error) error {
	return o.send(ctx, 1, err.Error(), nil)
}

func (o *icingaOutput) SetError(ctx context.Context, err error) error {
	return o.send(ctx, 2, err.Error(), nil)
}

func (o *icingaOutput) SetUnknown(ctx context.Context, err error) error {
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

	payload, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("unable to marshal the status: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v1/actions/process-check-result", o.apiURL), bytes.NewReader(payload))
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
		return fmt.Errorf("unable to send the status: %w", err)
	}
	defer func() {
		if closeErr := res.Body.Close(); closeErr != nil {
			err = fmt.Errorf("failed to close response body: %w", closeErr)
		}
	}()

	if res.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			return fmt.Errorf("unexpected status code: %d (failed to read response body: %w)", res.StatusCode, readErr)
		}
		return fmt.Errorf("unexpected status code: %d, response: %s", res.StatusCode, string(bodyBytes))
	}

	return err
}
