/*
Copyright 2018 The Doctl Authors All rights reserved.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package doctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/digitalocean/doctl/pkg/listen"
	"github.com/digitalocean/doctl/pkg/runner"
	"github.com/digitalocean/doctl/pkg/ssh"
	"github.com/digitalocean/doctl/pkg/terminal"
	"github.com/digitalocean/godo"
	"github.com/docker/docker/client"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"

	"github.com/digitalocean/doctl/internal/apps/builder"
)

const (
	// LatestReleaseURL is the latest release URL endpoint.
	LatestReleaseURL = "https://api.github.com/repos/digitalocean/doctl/releases/latest"
)

// Version is the version info for doit.
type Version struct {
	Major, Minor, Patch int
	Name, Build, Label  string
}

var (
	// Build is set at build time. It defines the git SHA for the current
	// build.
	Build string

	// Major is set at build time. It defines the major semantic version of
	// doctl.
	Major string

	// Minor is set at build time. It defines the minor semantic version of
	// doctl.
	Minor string

	// Patch is set at build time. It defines the patch semantic version of
	// doctl.
	Patch string

	// Label is set at build time. It defines the string that comes after the
	// version of doctl, ie, the "dev" in v1.0.0-dev.
	Label string

	// DoitVersion is doctl's version.
	DoitVersion Version

	// TraceHTTP traces http connections.
	TraceHTTP bool
)

func init() {
	if Build != "" {
		DoitVersion.Build = Build
	}
	if Major != "" {
		i, _ := strconv.Atoi(Major)
		DoitVersion.Major = i
	}
	if Minor != "" {
		i, _ := strconv.Atoi(Minor)
		DoitVersion.Minor = i
	}
	if Patch != "" {
		i, _ := strconv.Atoi(Patch)
		DoitVersion.Patch = i
	}
	if Label == "" {
		DoitVersion.Label = "dev"
	} else {
		DoitVersion.Label = Label
	}
}

func (v Version) String() string {
	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch))
	if v.Label != "" {
		buffer.WriteString("-" + v.Label)
	}

	return buffer.String()
}

// Complete is the complete version for doit.
func (v Version) Complete(lv LatestVersioner) string {
	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("doctl version %s", v.String()))

	if v.Build != "" {
		buffer.WriteString(fmt.Sprintf("\nGit commit hash: %s", v.Build))
	}

	if tagName, err := lv.LatestVersion(); err == nil {
		v0, err1 := semver.Make(tagName)
		v1, err2 := semver.Make(v.String())

		if len(v0.Build) == 0 {
			v0, err1 = semver.Make(tagName + "-release")
		}

		if err1 == nil && err2 == nil && v0.GT(v1) {
			buffer.WriteString(fmt.Sprintf("\nrelease %s is available, check it out! ", tagName))
		}
	}

	return buffer.String()
}

// CompleteJSON is the complete version for doit, formatted as JSON.
func (v Version) CompleteJSON(lv LatestVersioner) string {
	versionInfo := &struct {
		Version       string `json:"version,omitempty"`
		Commit        string `json:"commit,omitempty"`
		LatestRelease string `json:"latestRelease"`
		Notification  string `json:"notification,omitempty"`
	}{
		Version: v.String(),
		Commit:  v.Build,
	}

	if tagName, err := lv.LatestVersion(); err == nil {
		versionInfo.LatestRelease = tagName

		v0, err1 := semver.Make(tagName)
		v1, err2 := semver.Make(v.String())

		if len(v0.Build) == 0 {
			v0, err1 = semver.Make(tagName + "-release")
		}

		if err1 == nil && err2 == nil && v0.GT(v1) {
			versionInfo.Notification = fmt.Sprintf("release %s is available, check it out!", tagName)
		}
	}

	data, _ := json.MarshalIndent(versionInfo, "", "  ")
	return string(data)
}

// LatestVersioner an interface for detecting the latest version.
type LatestVersioner interface {
	LatestVersion() (string, error)
}

// GithubLatestVersioner retrieves the latest version from GitHub.
type GithubLatestVersioner struct{}

var _ LatestVersioner = &GithubLatestVersioner{}

// LatestVersion retrieves the latest version from Github or returns
// an error.
func (glv *GithubLatestVersioner) LatestVersion() (string, error) {
	u := LatestReleaseURL
	res, err := http.Get(u)
	if err != nil {
		return "", err
	}

	defer res.Body.Close()

	var m map[string]any
	if err = json.NewDecoder(res.Body).Decode(&m); err != nil {
		return "", err
	}

	tn, ok := m["tag_name"]
	if !ok {
		return "", errors.New("could not find tag name in response")
	}

	tagName := tn.(string)
	return strings.TrimPrefix(tagName, "v"), nil
}

// Config is an interface that represent doit's config.
type Config interface {
	GetGodoClient(trace, allowRetries bool, accessToken string) (*godo.Client, error)
	GetDockerEngineClient() (builder.DockerEngineClient, error)
	SSH(user, host, keyPath string, port int, opts ssh.Options) runner.Runner
	Listen(url *url.URL, token string, schemaFunc listen.SchemaFunc, out io.Writer, inCh <-chan []byte) listen.ListenerService
	Terminal() terminal.Terminal
	Set(ns, key string, val any)
	IsSet(key string) bool
	GetString(ns, key string) (string, error)
	GetBool(ns, key string) (bool, error)
	GetBoolPtr(ns, key string) (*bool, error)
	GetInt(ns, key string) (int, error)
	GetIntPtr(ns, key string) (*int, error)
	GetFloat64(ns, key string) (float64, error)
	GetStringSlice(ns, key string) ([]string, error)
	GetStringSliceIsFlagSet(ns, key string) ([]string, bool, error)
	GetStringMapString(ns, key string) (map[string]string, error)
	GetDuration(ns, key string) (time.Duration, error)
}

// LiveConfig is an implementation of Config for live values.
type LiveConfig struct {
	cliArgs map[string]bool
}

var _ Config = &LiveConfig{}

// GetGodoClient returns a GodoClient.
func (c *LiveConfig) GetGodoClient(trace, allowRetries bool, accessToken string) (*godo.Client, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required. (hint: run 'doctl auth init')")
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken})
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)

	args := []godo.ClientOpt{
		godo.SetUserAgent(userAgent()),
	}

	logger := log.New(os.Stderr, "doctl: ", log.LstdFlags)

	retryMax := viper.GetInt("http-retry-max")
	retryWaitMax := viper.GetInt("http-retry-wait-max")
	retryWaitMin := viper.GetInt("http-retry-wait-min")
	if retryMax > 0 && allowRetries {
		retryConfig := godo.RetryConfig{
			RetryMax: retryMax,
		}

		if retryWaitMax > 0 {
			retryConfig.RetryWaitMax = godo.PtrTo(float64(retryWaitMax))
		}

		if retryWaitMin > 0 {
			retryConfig.RetryWaitMin = godo.PtrTo(float64(retryWaitMin))
		}

		if trace {
			retryConfig.Logger = logger
		}

		args = append(args, godo.WithRetryAndBackoffs(retryConfig))
	}

	apiURL := viper.GetString("api-url")
	if apiURL != "" {
		args = append(args, godo.SetBaseURL(apiURL))
	}

	client, err := godo.New(oauthClient, args...)
	if err != nil {
		return nil, err
	}

	if trace {
		r := newRecorder(client.HTTPClient.Transport)

		go func() {
			for {
				select {
				case msg := <-r.req:
					logger.Println("->", strconv.Quote(msg))
				case msg := <-r.resp:
					logger.Println("<-", strconv.Quote(msg))
				}
			}
		}()

		client.HTTPClient.Transport = r
	}

	return client, nil
}

// GetDockerEngineClient returns a container engine client.
func (c *LiveConfig) GetDockerEngineClient() (builder.DockerEngineClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return cli, nil
}

func userAgent() string {
	return fmt.Sprintf("doctl/%s (%s %s)", DoitVersion.String(), runtime.GOOS, runtime.GOARCH)
}

// SSH creates a ssh connection to a host.
func (c *LiveConfig) SSH(user, host, keyPath string, port int, opts ssh.Options) runner.Runner {
	return &ssh.Runner{
		User:            user,
		Host:            host,
		KeyPath:         keyPath,
		Port:            port,
		AgentForwarding: opts[ArgsSSHAgentForwarding].(bool),
		Command:         opts[ArgSSHCommand].(string),
		RetriesMax:      opts[ArgSSHRetryMax].(int),
	}
}

// Listen creates a websocket connection
func (c *LiveConfig) Listen(url *url.URL, token string, schemaFunc listen.SchemaFunc, out io.Writer, inCh <-chan []byte) listen.ListenerService {
	return listen.NewListener(url, token, schemaFunc, out, inCh)
}

// Terminal returns a terminal.
func (c *LiveConfig) Terminal() terminal.Terminal {
	return terminal.New()
}

// Set sets a config key.
func (c *LiveConfig) Set(ns, key string, val any) {
	viper.Set(nskey(ns, key), val)
}

// IsSet checks if a config is set
func (c *LiveConfig) IsSet(key string) bool {
	matches := regexp.MustCompile("\b*--([a-z-_]+)").FindAllStringSubmatch(strings.Join(os.Args, " "), -1)
	if len(matches) == 0 {
		return false
	}

	if len(c.cliArgs) == 0 {
		args := make(map[string]bool)
		for _, match := range matches {
			args[match[1]] = true
		}
		c.cliArgs = args
	}
	return c.cliArgs[key]
}

// GetString returns a config value as a string.
func (c *LiveConfig) GetString(ns, key string) (string, error) {
	nskey := nskey(ns, key)
	str := viper.GetString(nskey)

	if isRequired(nskey) && strings.TrimSpace(str) == "" {
		return "", NewMissingArgsErr(nskey)
	}
	return str, nil
}

// GetBool returns a config value as a bool.
func (c *LiveConfig) GetBool(ns, key string) (bool, error) {
	return viper.GetBool(nskey(ns, key)), nil
}

// GetBoolPtr returns a config value as a bool pointer.
func (c *LiveConfig) GetBoolPtr(ns, key string) (*bool, error) {
	if !c.IsSet(key) {
		return nil, nil
	}
	val := viper.GetBool(nskey(ns, key))
	return &val, nil
}

// GetInt returns a config value as an int.
func (c *LiveConfig) GetInt(ns, key string) (int, error) {
	nskey := nskey(ns, key)
	val := viper.GetInt(nskey)

	if isRequired(nskey) && val == 0 {
		return 0, NewMissingArgsErr(nskey)
	}
	return val, nil
}

// GetIntPtr returns a config value as an int pointer.
func (c *LiveConfig) GetIntPtr(ns, key string) (*int, error) {
	nskey := nskey(ns, key)

	if !c.IsSet(key) {
		if isRequired(nskey) {
			return nil, NewMissingArgsErr(nskey)
		}
		return nil, nil
	}
	val := viper.GetInt(nskey)
	return &val, nil
}

// GetFloat64 returns a config value as a float64.
func (c *LiveConfig) GetFloat64(ns, key string) (float64, error) {
	nskey := nskey(ns, key)
	val := viper.GetFloat64(nskey)

	if isRequired(nskey) && val == 0.0 {
		return 0.0, NewMissingArgsErr(nskey)
	}
	return val, nil
}

// GetStringSlice returns a config value as a string slice.
func (c *LiveConfig) GetStringSlice(ns, key string) ([]string, error) {
	nskey := nskey(ns, key)
	val := viper.GetStringSlice(nskey)

	if isRequired(nskey) && emptyStringSlice(val) {
		return nil, NewMissingArgsErr(nskey)
	}

	out := []string{}
	for _, item := range viper.GetStringSlice(nskey) {
		item = strings.TrimPrefix(item, "[")
		item = strings.TrimSuffix(item, "]")

		list := strings.Split(item, ",")
		for _, str := range list {
			if str == "" {
				continue
			}
			out = append(out, str)
		}
	}
	return out, nil
}

// GetStringSliceIsFlagSet returns a config value as a string slice and a bool representing the existence of the flag.
func (c *LiveConfig) GetStringSliceIsFlagSet(ns, key string) ([]string, bool, error) {
	if !c.IsSet(key) {
		return nil, false, nil
	}
	strSlice, err := c.GetStringSlice(ns, key)
	return strSlice, true, err
}

// GetStringMapString returns a config value as a string to string map.
func (c *LiveConfig) GetStringMapString(ns, key string) (map[string]string, error) {
	nskey := nskey(ns, key)

	if isRequired(nskey) && !c.IsSet(key) {
		return nil, NewMissingArgsErr(nskey)
	}

	// We cannot call viper.GetStringMapString because it does not handle
	// pflag's StringToStringP properly:
	// https://github.com/spf13/viper/issues/608
	// Re-implement the necessary pieces on our own instead.

	vals := map[string]string{}
	items := viper.GetStringSlice(nskey)
	for _, item := range items {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) < 2 {
			return nil, fmt.Errorf("item %q does not adhere to form: key=value", item)
		}
		labelKey := parts[0]
		labelValue := parts[1]
		vals[labelKey] = labelValue
	}

	return vals, nil
}

// GetDuration returns a config value as a duration.
func (c *LiveConfig) GetDuration(ns, key string) (time.Duration, error) {
	return viper.GetDuration(nskey(ns, key)), nil
}

func nskey(ns, key string) string {
	if ns != "" {
		key = fmt.Sprintf("%s.%s", ns, key)
	}
	return key
}

func isRequired(key string) bool {
	return viper.GetBool(fmt.Sprintf("required.%s", key))
}

// TestConfig is an implementation of Config for testing.
type TestConfig struct {
	SSHFn              func(user, host, keyPath string, port int, opts ssh.Options) runner.Runner
	ListenFn           func(url *url.URL, token string, schemaFunc listen.SchemaFunc, out io.Writer, inCh <-chan []byte) listen.ListenerService
	TerminalFn         func() terminal.Terminal
	v                  *viper.Viper
	IsSetMap           map[string]bool
	DockerEngineClient builder.DockerEngineClient
}

var _ Config = &TestConfig{}

// NewTestConfig creates a new, ready-to-use instance of a TestConfig.
func NewTestConfig() *TestConfig {
	return &TestConfig{
		SSHFn: func(u, h, kp string, p int, opts ssh.Options) runner.Runner {
			return &MockRunner{}
		},
		ListenFn: func(url *url.URL, token string, schemaFunc listen.SchemaFunc, out io.Writer, inCh <-chan []byte) listen.ListenerService {
			return &MockListener{}
		},
		TerminalFn: func() terminal.Terminal {
			return &MockTerminal{}
		},
		v:        viper.New(),
		IsSetMap: make(map[string]bool),
	}
}

// GetGodoClient mocks a GetGodoClient call. The returned godo client will
// be nil.
func (c *TestConfig) GetGodoClient(trace, allowRetries bool, accessToken string) (*godo.Client, error) {
	return &godo.Client{}, nil
}

// GetDockerEngineClient mocks a GetDockerEngineClient call. The returned client will
// be nil unless configured in the test config.
func (c *TestConfig) GetDockerEngineClient() (builder.DockerEngineClient, error) {
	return c.DockerEngineClient, nil
}

// SSH returns a mock SSH runner.
func (c *TestConfig) SSH(user, host, keyPath string, port int, opts ssh.Options) runner.Runner {
	return c.SSHFn(user, host, keyPath, port, opts)
}

// Listen returns a mock websocket listener
func (c *TestConfig) Listen(url *url.URL, token string, schemaFunc listen.SchemaFunc, out io.Writer, inCh <-chan []byte) listen.ListenerService {
	return c.ListenFn(url, token, schemaFunc, out, inCh)
}

// Terminal returns a mock terminal.
func (c *TestConfig) Terminal() terminal.Terminal {
	return c.TerminalFn()
}

// Set sets a config key.
func (c *TestConfig) Set(ns, key string, val any) {
	nskey := nskey(ns, key)
	c.v.Set(nskey, val)
	c.IsSetMap[key] = true
}

// IsSet returns true if the given key is set on the config.
func (c *TestConfig) IsSet(key string) bool {
	return c.IsSetMap[key]
}

// GetString returns the string value for the key in the given namespace. Because
// this is a mock implementation, and error will never be returned.
func (c *TestConfig) GetString(ns, key string) (string, error) {
	nskey := nskey(ns, key)
	return c.v.GetString(nskey), nil
}

// GetInt returns the int value for the key in the given namespace. Because
// this is a mock implementation, and error will never be returned.
func (c *TestConfig) GetInt(ns, key string) (int, error) {
	nskey := nskey(ns, key)
	return c.v.GetInt(nskey), nil
}

// GetIntPtr returns the int value for the key in the given namespace. Because
// this is a mock implementation, and error will never be returned.
func (c *TestConfig) GetIntPtr(ns, key string) (*int, error) {
	nskey := nskey(ns, key)
	if !c.v.IsSet(nskey) {
		return nil, nil
	}
	val := c.v.GetInt(nskey)
	return &val, nil
}

// GetFloat64 returns the float64 value for the key in the given namespace. Because
// this is a mock implementation, and error will never be returned.
func (c *TestConfig) GetFloat64(ns, key string) (float64, error) {
	nskey := nskey(ns, key)
	return c.v.GetFloat64(nskey), nil
}

// GetStringSlice returns the string slice value for the key in the given
// namespace. Because this is a mock implementation, and error will never be
// returned.
func (c *TestConfig) GetStringSlice(ns, key string) ([]string, error) {
	nskey := nskey(ns, key)
	return c.v.GetStringSlice(nskey), nil
}

// GetStringSliceIsFlagSet returns the string slice value for the key in the given
// namespace and a bool representing the existence of the flag. Because this is a mock implementation,
// and error will never be returned.
func (c *TestConfig) GetStringSliceIsFlagSet(ns, key string) ([]string, bool, error) {
	nskey := nskey(ns, key)
	if !c.v.IsSet(nskey) {
		return nil, false, nil
	}
	return c.v.GetStringSlice(nskey), true, nil
}

// GetStringMapString returns the string-to-string value for the key in the
// given namespace. Because this is a mock implementation, and error will never
// be returned.
func (c *TestConfig) GetStringMapString(ns, key string) (map[string]string, error) {
	nskey := nskey(ns, key)
	return c.v.GetStringMapString(nskey), nil
}

// GetBool returns the bool value for the key in the given namespace. Because
// this is a mock implementation, and error will never be returned.
func (c *TestConfig) GetBool(ns, key string) (bool, error) {
	nskey := nskey(ns, key)
	return c.v.GetBool(nskey), nil
}

// GetBoolPtr returns the bool value for the key in the given namespace. Because
// this is a mock implementation, and error will never be returned.
func (c *TestConfig) GetBoolPtr(ns, key string) (*bool, error) {
	nskey := nskey(ns, key)
	if !c.v.IsSet(nskey) {
		return nil, nil
	}
	val := c.v.GetBool(nskey)
	return &val, nil
}

// GetDuration returns the duration value for the key in the given namespace. Because
// this is a mock implementation, and error will never be returned.
func (c *TestConfig) GetDuration(ns, key string) (time.Duration, error) {
	nskey := nskey(ns, key)
	return c.v.GetDuration(nskey), nil
}

// This is needed because an empty StringSlice flag returns `"[]"`
func emptyStringSlice(s []string) bool {
	return (len(s) == 1 && s[0] == "[]") || len(s) == 0
}

// CommandName returns the name by which doctl was invoked
func CommandName() string {
	name, ok := os.LookupEnv("SNAP_NAME")
	if !ok || name != "doctl" {
		return os.Args[0]
	}
	return name
}
