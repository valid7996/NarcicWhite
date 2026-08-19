package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
	"unicode"

	"narcicwhite-desktop/internal/model"
	"narcicwhite-desktop/internal/profiles"
)

const (
	narcicWhiteSubscriptionID              = model.BuiltInSubscriptionID
	narcicWhiteSubscriptionName            = "Narcic White"
	narcicWhiteSubscriptionRefreshInterval = 3 * time.Hour

	narcicWhiteFrontingPingLimit       = 96
	narcicWhiteFrontingValidationLimit = 3
	narcicWhiteFrontingValidationTime  = 8 * time.Second
	narcicWhiteStartupWorkingSample    = 5
)

// Where the built-in catalogue comes from, and the key that opens it. Both are
// set at link time by the Makefile:
//
//	-X main.narcicWhiteSubscriptionURL=... -X main.narcicWhiteSubscriptionKey=...
//
// They used to be constants in this file, which is in a public repository, and
// had been since its first commit. That is not a weak secret — it is not a
// secret at all: anyone could read the key off a web page, fetch the encrypted
// catalogue and decrypt it, and what falls out is every node's address, UUID,
// password and REALITY keys. No binary needed, nothing to reverse engineer. A
// censor gets the complete list to block; anyone else gets the service for free.
//
// Moving them here does not make them secret either, and it must not be sold as
// though it does. They still travel inside every binary that ships, and a client
// that can decrypt the catalogue is a client an attacker can take apart. What it
// changes is the effort: reading a public file becomes pulling strings out of a
// 74 MB executable. That is worth doing, and it is the most that can be done
// while the client holds the key at all.
//
// The old values remain in this repository's history for ever, so this is only
// worth anything once the key on the server has been changed.
//
// A build made without them — `go build`, `wails build`, or anyone building from
// source — has no catalogue and says so. Manual configs and a user's own
// subscriptions are unaffected, which is the right shape for an open-source
// client of a managed service.
var (
	narcicWhiteSubscriptionURL string
	narcicWhiteSubscriptionKey string
)

// errNoBuiltInCatalogue is what a build without the catalogue credentials says
// when something asks for them.
var errNoBuiltInCatalogue = errors.New(
	"this build has no NarcicWhite catalogue: it was not built with one. Add a subscription of your own, or paste configs on the Servers page")

// builtInCatalogueAvailable reports whether this build can reach the catalogue
// at all.
func builtInCatalogueAvailable() bool {
	return strings.TrimSpace(narcicWhiteSubscriptionURL) != "" &&
		strings.TrimSpace(narcicWhiteSubscriptionKey) != ""
}

type narcicWhiteSubscriptionFetcher func(context.Context) (string, error)
type narcicWhiteFrontingIPFetcher func(context.Context) (string, error)
type narcicWhiteFrontingRanker func(context.Context, model.V2RayProfile, []string) []string
type narcicWhiteFrontingValidator func(context.Context, model.V2RayProfile) model.V2RayPingResult

type narcicWhiteEncryptedPayload struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	Encoding   string `json:"encoding"`
	IV         string `json:"iv"`
	Ciphertext string `json:"ciphertext"`
}

type narcicWhiteRuntimeSelection struct {
	storedProfile  model.V2RayProfile
	runtimeProfile model.V2RayProfile
	startupLogs    []string
}

type narcicWhiteStartupExclusion struct {
	profileID  string
	frontingIP string
}

func fetchNarcicWhiteSubscriptionDocument(ctx context.Context) (string, error) {
	if !builtInCatalogueAvailable() {
		return "", errNoBuiltInCatalogue
	}
	body, err := fetchV2RaySubscriptionDocument(ctx, narcicWhiteSubscriptionURL)
	if err == nil {
		return body, nil
	}

	// The same fallback the user's own subscriptions get, and this path needs it
	// most: connecting is what fetches this, so there is never a tunnel to fall
	// back to here. A network that reads the ClientHello and cuts it leaves the
	// app with no node list, which is to say no app at all.
	if looksLikeInterference(err) {
		if fragmented := fragmentedDirectClient(false); fragmented != nil {
			if body, retryErr := fetchV2RaySubscriptionDocumentWith(ctx, narcicWhiteSubscriptionURL, fragmented); retryErr == nil {
				return body, nil
			}
		}
	}
	return "", err
}

func (a *App) StartNarcicWhiteConnection() (model.AppState, error) {
	// The gate is enforced here and not only in the interface. A gate that only
	// the interface applies is one that is not really there.
	if state := a.GetAppState(); !privacyPolicyAccepted(state) {
		return state, fmt.Errorf("the privacy policy has not been accepted yet")
	}

	a.mu.Lock()
	if a.state.Runtime.Status != model.RuntimeDisconnected && a.state.Runtime.Status != model.RuntimeFailed {
		state := a.state
		a.mu.Unlock()
		return state, nil
	}
	a.mu.Unlock()

	return a.startNarcicWhiteWithMihomo()
}

// RefreshNarcicWhiteConnection reconnects. A session holds no stored profile to
// exclude and picks its node when it connects, so stopping and starting again is
// what refreshing means here.
func (a *App) RefreshNarcicWhiteConnection() (model.AppState, error) {
	if _, err := a.StopConnection(); err != nil {
		return a.GetAppState(), err
	}
	return a.StartNarcicWhiteConnection()
}

func (a *App) SaveNarcicWhiteFrontingIPs(rawText string) (model.AppState, error) {
	ips, err := parseNarcicWhiteCustomFrontingIPs(rawText)
	if err != nil {
		return a.GetAppState(), err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.NarcicWhiteFrontingIPs = ips
	return a.saveLocked()
}

// The subscription the app connects through.
//
// The built-in catalogue arrives encrypted from an address held in code; one the
// user added arrives as whatever they pointed at — share links, base64 of them,
// or a mihomo document — and session.PrepareConfig works out which. Both come
// through here so that the connect path and the connection dialog can never be
// looking at different lists.

// SelectSubscription chooses which server source the VPN connects through.
func (a *App) SelectSubscription(id string) (model.AppState, error) {
	id = strings.TrimSpace(id)
	a.mu.Lock()
	manualAvailable := id == model.ManualServerSourceID && slices.ContainsFunc(a.state.V2RayProfiles, func(profile model.V2RayProfile) bool {
		return profile.SubscriptionID == ""
	})
	if _, ok := findV2RaySubscription(a.state, id); !ok && id != narcicWhiteSubscriptionID && !manualAvailable {
		state := a.state
		a.mu.Unlock()
		return state, fmt.Errorf("that server source is not in the list")
	}
	if a.state.SelectedSubscriptionID == id {
		state := a.state
		a.mu.Unlock()
		return state, nil
	}
	if a.mihomo.current() != nil {
		// The running tunnel was built from the current subscription's servers
		// and cannot be moved onto another's. Changing the setting under it
		// would leave the app naming one subscription while carrying traffic
		// through a different one.
		state := a.state
		a.mu.Unlock()
		return state, fmt.Errorf("disconnect before changing the subscription: the connection is running on the current one")
	}
	a.state.SelectedSubscriptionID = id
	// The dashboard's node choice belongs to the subscription it was made in.
	a.state.NarcicWhite.Connection.Node = ""
	state, err := a.saveLocked()
	a.mu.Unlock()

	return state, err
}

func (a *App) selectedSubscriptionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := strings.TrimSpace(a.state.SelectedSubscriptionID)
	if id == "" {
		return narcicWhiteSubscriptionID
	}
	return id
}

// subscriptionBody fetches the selected subscription, ready for the engine.
func (a *App) subscriptionBody(ctx context.Context) (string, error) {
	return a.subscriptionBodyFor(ctx, a.selectedSubscriptionID())
}

// subscriptionBodyFor fetches one by name, which is what lets the Servers page
// look at a subscription the VPN is not connecting through.
func (a *App) subscriptionBodyFor(ctx context.Context, id string) (string, error) {
	if id == model.ManualServerSourceID {
		a.mu.Lock()
		manualProfiles := make([]model.V2RayProfile, 0, len(a.state.V2RayProfiles))
		for _, profile := range a.state.V2RayProfiles {
			if profile.SubscriptionID == "" {
				manualProfiles = append(manualProfiles, profile)
			}
		}
		a.mu.Unlock()
		body, err := profiles.ExportV2RayProfiles(manualProfiles)
		if err != nil {
			return "", fmt.Errorf("manual configs unavailable: %w", err)
		}
		return body, nil
	}
	if id == narcicWhiteSubscriptionID {
		raw, err := fetchNarcicWhiteSubscriptionDocument(ctx)
		if err != nil {
			return "", fmt.Errorf("subscription unavailable: %w", err)
		}
		body, err := decryptNarcicWhiteSubscription(raw, narcicWhiteSubscriptionKey)
		if err != nil {
			return "", fmt.Errorf("subscription unreadable: %w", err)
		}
		return body, nil
	}

	a.mu.Lock()
	subscription, ok := findV2RaySubscription(a.state, id)
	a.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("the selected subscription is no longer in the list")
	}
	body, err := a.fetchSubscriptionDocument(ctx, subscription)
	if err != nil {
		return "", fmt.Errorf("subscription unavailable: %w", err)
	}
	return body, nil
}

func decryptNarcicWhiteSubscription(rawText string, passphrase string) (string, error) {
	return decryptNarcicWhitePayload(rawText, passphrase, "subscription")
}

func decryptNarcicWhiteIPList(rawText string, passphrase string) (string, error) {
	return decryptNarcicWhitePayload(rawText, passphrase, "IP list")
}

func decryptNarcicWhitePayload(rawText string, passphrase string, label string) (string, error) {
	var payload narcicWhiteEncryptedPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawText)), &payload); err != nil {
		return "", err
	}
	if payload.Version != 1 {
		return "", fmt.Errorf("unsupported Narcic White %s version", label)
	}
	if payload.Algorithm != "AES-GCM" {
		return "", fmt.Errorf("unsupported Narcic White %s algorithm", label)
	}
	if payload.Encoding != "base64url" {
		return "", fmt.Errorf("unsupported Narcic White %s encoding", label)
	}
	iv, err := decodeNarcicWhiteBase64URL(payload.IV)
	if err != nil {
		return "", fmt.Errorf("invalid Narcic White %s iv: %w", label, err)
	}
	ciphertext, err := decodeNarcicWhiteBase64URL(payload.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("invalid Narcic White %s ciphertext: %w", label, err)
	}
	key := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("unable to decrypt Narcic White %s: %w", label, err)
	}
	return string(plaintext), nil
}

func parseNarcicWhiteFrontingIPs(rawText string) ([]string, error) {
	rawText = strings.TrimSpace(rawText)
	if rawText == "" {
		return nil, fmt.Errorf("Narcic White fronting IP list is empty")
	}
	var values []string
	var decoded any
	if err := json.Unmarshal([]byte(rawText), &decoded); err == nil {
		collectNarcicWhiteIPStrings(decoded, &values)
	} else {
		values = strings.FieldsFunc(rawText, func(r rune) bool {
			return r == ',' || unicode.IsSpace(r)
		})
	}

	seen := map[string]struct{}{}
	ips := make([]string, 0, len(values))
	for _, value := range values {
		ip := net.ParseIP(strings.Trim(strings.TrimSpace(value), `"'`))
		if ip == nil || ip.To4() == nil {
			continue
		}
		normalized := ip.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		ips = append(ips, normalized)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("Narcic White fronting IP list did not contain IPv4 addresses")
	}
	return ips, nil
}

func parseNarcicWhiteCustomFrontingIPs(rawText string) ([]string, error) {
	rawText = strings.TrimSpace(rawText)
	if rawText == "" {
		return []string{}, nil
	}
	seen := map[string]struct{}{}
	ips := make([]string, 0, profiles.MaxNarcicWhiteFrontingIPs)
	for _, part := range strings.Split(rawText, ",") {
		part = strings.TrimSpace(part)
		if part == "" || strings.ContainsFunc(part, unicode.IsSpace) {
			return nil, fmt.Errorf("Fronting IPs must be comma-separated IPv4 addresses")
		}
		ip := net.ParseIP(part)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("Fronting IP must be a valid IPv4 address")
		}
		normalized := ip.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		ips = append(ips, normalized)
		if len(ips) > profiles.MaxNarcicWhiteFrontingIPs {
			return nil, fmt.Errorf("Fronting IP accepts up to %d IPv4 addresses", profiles.MaxNarcicWhiteFrontingIPs)
		}
	}
	return ips, nil
}

func collectNarcicWhiteIPStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case []any:
		for _, item := range typed {
			collectNarcicWhiteIPStrings(item, out)
		}
	case map[string]any:
		for _, item := range typed {
			collectNarcicWhiteIPStrings(item, out)
		}
	}
}

func (a *App) narcicWhiteFrontingIPsSnapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return profiles.NormalizeNarcicWhiteFrontingIPs(a.state.NarcicWhiteFrontingIPs)
}

func narcicWhiteProfileHost(profile model.V2RayProfile) string {
	if host := strings.TrimSpace(profile.SNI); host != "" {
		return host
	}
	if host := firstNarcicWhiteHeaderHost(profile.TransportHost); host != "" {
		return host
	}
	return strings.TrimSpace(profile.Server)
}

func firstNarcicWhiteHeaderHost(value string) string {
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	}) {
		part = strings.TrimSpace(part)
		if part != "" {
			return part
		}
	}
	return ""
}

func narcicWhiteHTTPFrontingTransport(network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "ws", "websocket", "grpc", "httpupgrade", "xhttp", "splithttp", "http", "h2":
		return true
	default:
		return false
	}
}

func decodeNarcicWhiteBase64URL(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

// ensureNarcicWhiteSubscriptionLocked keeps the built-in catalogue listed among
// the subscriptions.
//
// Its address is deliberately not stored. The app knows it as a constant and
// fetches it from there, so leaving it out of the state means there is nowhere
// for it to be read from: not the subscriptions list, not a backup export, and
// not the state the interface is handed. A subscription the user adds is
// theirs and is stored and shown as they typed it.
func (a *App) ensureNarcicWhiteSubscriptionLocked() int {
	idx := findV2RaySubscriptionIndex(a.state.V2RaySubscriptions, narcicWhiteSubscriptionID)
	if idx == -1 {
		a.state.V2RaySubscriptions = append(a.state.V2RaySubscriptions, model.V2RaySubscription{
			ID:   narcicWhiteSubscriptionID,
			Name: narcicWhiteSubscriptionName,
		})
		return len(a.state.V2RaySubscriptions) - 1
	}
	a.state.V2RaySubscriptions[idx].Name = narcicWhiteSubscriptionName
	// Clears it from a state file written before this was true.
	a.state.V2RaySubscriptions[idx].URL = ""
	return idx
}

// refreshNarcicWhiteCatalogue re-fetches the built-in catalogue on demand.
//
// The generic subscription refresh cannot do this one: it fetches whatever
// address is stored, and this one has none stored, arrives encrypted, and is
// counted in nodes rather than in stored profiles.
func (a *App) refreshNarcicWhiteCatalogue() (model.V2RaySubscriptionRefreshResult, error) {
	list, err := a.ListNarcicWhiteNodes(true)
	if err != nil {
		a.mu.Lock()
		a.recordNarcicWhiteSubscriptionErrorLocked(err)
		next, saveErr := a.saveLocked()
		a.mu.Unlock()
		return model.V2RaySubscriptionRefreshResult{
			State:        next,
			Subscription: findV2RaySubscriptionOrZero(next, narcicWhiteSubscriptionID),
			Message:      err.Error(),
		}, saveErr
	}

	a.mu.Lock()
	idx := a.ensureNarcicWhiteSubscriptionLocked()
	a.state.V2RaySubscriptions[idx].ImportedCount = len(list.Nodes)
	a.state.V2RaySubscriptions[idx].LastUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	a.state.V2RaySubscriptions[idx].LastError = ""
	next, saveErr := a.saveLocked()
	a.mu.Unlock()

	return model.V2RaySubscriptionRefreshResult{
		State:        next,
		Subscription: findV2RaySubscriptionOrZero(next, narcicWhiteSubscriptionID),
		OK:           true,
		Message:      fmt.Sprintf("%d nodes available.", len(list.Nodes)),
		Imported:     len(list.Nodes),
	}, saveErr
}

// forgetBuiltInCatalogueProfiles drops the stored copy of the catalogue.
//
// It used to be kept as V2RayProfiles so the Xray path could connect through
// one of them. Nothing fills it now and nothing reads it, so what is left in an
// older state file is a frozen list from whenever it was last written — and it
// was being counted and shown as though it were the catalogue. A subscription
// the user added keeps its profiles: those are theirs.
func forgetBuiltInCatalogueProfiles(state model.AppState) model.AppState {
	kept := make([]model.V2RayProfile, 0, len(state.V2RayProfiles))
	for _, profile := range state.V2RayProfiles {
		if profile.SubscriptionID == narcicWhiteSubscriptionID {
			continue
		}
		kept = append(kept, profile)
	}
	state.V2RayProfiles = kept
	return state
}

// forgetBuiltInSubscriptionURL strips the catalogue's address from a state that
// came from somewhere else — a file written by an older build, or a restored
// backup. Without it, hiding the address would only apply to states this build
// created.
func forgetBuiltInSubscriptionURL(state model.AppState) model.AppState {
	for idx := range state.V2RaySubscriptions {
		if state.V2RaySubscriptions[idx].ID == narcicWhiteSubscriptionID {
			state.V2RaySubscriptions[idx].URL = ""
		}
	}
	return state
}

func (a *App) recordNarcicWhiteSubscriptionErrorLocked(err error) {
	idx := a.ensureNarcicWhiteSubscriptionLocked()
	a.state.V2RaySubscriptions[idx].LastError = err.Error()
}
