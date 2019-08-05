package recaptcha

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/xerrors"
)

const DefaultURL = "https://www.google.com/recaptcha/api/siteverify"

// HTTPClient is a basic interface for an HTTP client, as required by this
// library. The standard *http.Client satisfies this interface.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client for making requests to the reCAPTCHA verification endpoint and
// receiving token verification responses. Created with NewClient.
type Client struct {
	secret string
	url    string
	client HTTPClient
}

// Option represents a configuration option that can be applied when creating a
// Client via the NewClient method.
type Option func(c *Client)

// SetHTTPClient is an option for creating a Client with a custom http.Client.
// If not provided, the Client will use http.DefaultClient.
func SetHTTPClient(client HTTPClient) Option {
	return func(c *Client) {
		c.client = client
	}
}

// SetURL is an option for creating a Client that hits a custom verification
// URL. If not provided, the Client will use DefaultURL.
func SetURL(url string) Option {
	return func(c *Client) {
		c.url = url
	}
}

// Creates an instance of Client, which is thread-safe and should be reused
// instead of created as needed. You must provided your secret key, which is
// shared between your site and reCAPTCHA.
func NewClient(secret string, opts ...Option) *Client {
	client := &Client{
		secret: secret,
		url:    DefaultURL,
		client: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

// Fetch makes a request to the reCAPTCHA verification endpoint using the
// provided token and optional userIP (which can be omitted from the request by
// providing an empty string), and returns the response. To check whether the
// token was actually valid, use the response's Verify method.
func (c *Client) Fetch(ctx context.Context, token, userIP string) (Response, error) {
	values := url.Values{
		"secret":   {c.secret},
		"response": {token},
	}
	if userIP != "" {
		values["remoteIP"] = []string{userIP}
	}

	request, err := http.NewRequest(http.MethodPost, c.url, strings.NewReader(values.Encode()))
	if err != nil {
		return Response{}, xerrors.Errorf("error creating POST request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request = request.WithContext(ctx)

	res, err := c.client.Do(request)
	if err != nil {
		return Response{}, xerrors.Errorf("error making POST request: %w", err)
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return Response{}, xerrors.Errorf("error reading response body: %w", err)
	}

	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		return Response{}, xerrors.Errorf("error unmarshalling response body: %w", err)
	}

	return response, nil
}

// Response represents a response from the reCAPTCHA token verification
// endpoint. The validity of the token can be verified via the Verify method.
type Response struct {
	Success     bool      `json:"success"`
	Score       float64   `json:"score"`
	Action      string    `json:"action"`
	ChallengeTs time.Time `json:"challenge_ts"`
	Hostname    string    `json:"hostname"`
	ErrorCodes  []string  `json:"error-codes"`
}

// Verify checks whether or not the response represents a valid token. It will
// return a *VerificationError if the response's "success" field is false, or
// if the "error-codes" field is non-empty. Further optional verification
// criteria may be provided, in which case their respective errors may be
// returned as well.
func (r *Response) Verify(criteria ...Criterion) error {
	if !r.Success || len(r.ErrorCodes) > 0 {
		return &VerificationError{
			ErrorCodes: r.ErrorCodes,
		}
	}

	for _, criterion := range criteria {
		if err := criterion(r); err != nil {
			return err
		}
	}

	return nil
}

// Criterion is an optional token verification criterion that can be applied
// when a token is verified via the Verify method.
type Criterion func(r *Response) error

// Hostname is an optional verification criterion which ensures that the
// hostname of the website where the reCAPTCHA was presented is the expected
// one. Returns *InvalidHostnameError if the hostname is not correct.
func Hostname(hostname string) Criterion {
	return func(r *Response) error {
		if r.Hostname != hostname {
			return &InvalidHostnameError{
				Expected: hostname,
				Actual:   r.Hostname,
			}
		}
		return nil
	}
}

// Action is an optional verification criterion which ensures that the website
// action associated with the reCAPTCHA is the expected one. Returns
// *InvalidActionError if the action is not correct.
func Action(action string) Criterion {
	return func(r *Response) error {
		if r.Action != action {
			return &InvalidActionError{
				Expected: action,
				Actual:   r.Action,
			}
		}
		return nil
	}
}

// Score is an optional verification criterion which ensures that the score
// associated with the reCAPTCHA meets a minimum threshold. Returns
// *InvalidScoreError if the score is below the threshold.
func Score(threshold float64) Criterion {
	return func(r *Response) error {
		if r.Score < threshold {
			return &InvalidScoreError{
				Threshold: threshold,
				Actual:    r.Score,
			}
		}
		return nil
	}
}

// Makes it possible to mock time.Now() calls
var now = time.Now

// Window is an optional verification criterion which ensures that the response
// token is being used within the specified window from the time that the
// reCAPTCHA was presented. By default, the reCAPTCHA verification endpoint
// only considers a token valid for 2 minutes, so this method is only necessary
// if you want to enforce an narrower window than that. Returns
// *InvalidChallengeTsError if the challenge timestamp is outside the valid
// window.
func Window(window time.Duration) Criterion {
	return func(r *Response) error {
		if diff := now().Sub(r.ChallengeTs); diff > window {
			return &InvalidChallengeTsError{
				ChallengeTs: r.ChallengeTs,
				Window:      window,
				Actual:      diff,
			}
		}
		return nil
	}
}
