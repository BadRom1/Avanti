package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/platform/config"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
	"github.com/Romain-Badino/Avanti/internal/platform/server"
)

// testConfig est la configuration minimale d'un serveur de test : port éphémère
// et arrêt bref.
func testConfig() *config.Config {
	return &config.Config{
		ListenAddr:      "127.0.0.1:0",
		ShutdownTimeout: 5 * time.Second,
	}
}

// newTestServer construit un serveur muni d'un journal muet.
func newTestServer(t *testing.T, handler http.Handler, ready server.ReadinessCheck) *server.Server {
	t.Helper()

	srv, err := server.New(server.Options{
		Config:  testConfig(),
		Logger:  logging.Discard(),
		Handler: handler,
		Ready:   ready,
	})
	if err != nil {
		t.Fatalf("server.New() a échoué : %v", err)
	}

	return srv
}

// response est une réponse déjà lue et refermée : les tests raisonnent sur son
// contenu sans avoir à gérer la fermeture du corps à chaque appel.
type response struct {
	Status int
	Header http.Header
	Body   string
}

// do exerce la chaîne complète du serveur sans ouvrir de port.
func do(t *testing.T, srv *server.Server, method, target string, headers map[string]string) response {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	result := rec.Result()
	defer func() {
		if err := result.Body.Close(); err != nil {
			t.Errorf("fermeture du corps de réponse : %v", err)
		}
	}()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	return response{Status: result.StatusCode, Header: result.Header, Body: string(body)}
}

// listen ouvre un port éphémère sur la boucle locale.
func listen(t *testing.T, ctx context.Context) net.Listener {
	t.Helper()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ouverture du port de test : %v", err)
	}

	return listener
}

// fetch exécute une requête réelle contre un serveur en écoute.
func fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}

	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}

	return string(body), err
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts server.Options
	}{
		{name: "sans configuration", opts: server.Options{Logger: logging.Discard()}},
		{name: "sans journal", opts: server.Options{Config: testConfig()}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := server.New(tc.opts); err == nil {
				t.Fatal("server.New() doit refuser une dépendance manquante")
			}
		})
	}
}

// TestHealthzIgnoresDatabase protège la distinction entre les deux sondes : la
// vivacité ne doit pas retomber parce que PostgreSQL a hoqueté, sinon
// l'orchestrateur redémarre un processus parfaitement sain.
func TestHealthzIgnoresDatabase(t *testing.T) {
	t.Parallel()

	probed := false
	srv := newTestServer(t, nil, func(context.Context) error {
		probed = true
		return errors.New("base injoignable")
	})

	resp := do(t, srv, http.MethodGet, "/healthz", nil)
	if resp.Status != http.StatusOK {
		t.Errorf("statut = %d, attendu 200", resp.Status)
	}
	if !strings.Contains(resp.Body, "ok") {
		t.Errorf("corps = %q", resp.Body)
	}
	if probed {
		t.Error("/healthz ne doit pas interroger la base")
	}
}

func TestReadyz(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		ready server.ReadinessCheck
		want  int
	}{
		{name: "sans sonde", ready: nil, want: http.StatusOK},
		{name: "sonde satisfaite", ready: func(context.Context) error { return nil }, want: http.StatusOK},
		{
			name:  "sonde en échec",
			ready: func(context.Context) error { return errors.New("base injoignable") },
			want:  http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := do(t, newTestServer(t, nil, tc.ready), http.MethodGet, "/readyz", nil)
			if resp.Status != tc.want {
				t.Errorf("statut = %d, attendu %d", resp.Status, tc.want)
			}
		})
	}
}

// TestReadyzHidesInfrastructureDetails vérifie qu'une sonde en échec ne raconte
// pas l'infrastructure au premier venu : /readyz est joignable sans
// authentification.
func TestReadyzHidesInfrastructureDetails(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, nil, func(context.Context) error {
		return errors.New("connexion refusée sur db-interne.local:5432")
	})

	resp := do(t, srv, http.MethodGet, "/readyz", nil)
	if strings.Contains(resp.Body, "db-interne.local") {
		t.Errorf("corps = %q, ne doit pas divulguer l'infrastructure", resp.Body)
	}
}

func TestApplicationHandlerIsMounted(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), nil)

	if resp := do(t, srv, http.MethodGet, "/une/page", nil); resp.Status != http.StatusTeapot {
		t.Errorf("statut = %d, le gestionnaire applicatif doit être servi", resp.Status)
	}
}

func TestRequestIDIsGeneratedAndExposed(t *testing.T) {
	t.Parallel()

	var seen string
	srv := newTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = server.RequestIDFromContext(r.Context())
	}), nil)

	resp := do(t, srv, http.MethodGet, "/", nil)

	if seen == "" {
		t.Fatal("le gestionnaire doit trouver un identifiant dans le contexte")
	}
	if got := resp.Header.Get(server.RequestIDHeader); got != seen {
		t.Errorf("en-tête %s = %q, attendu %q", server.RequestIDHeader, got, seen)
	}
}

func TestRequestIDInheritance(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		incoming  string
		preserved bool
	}{
		{name: "identifiant sain", incoming: "trace-42_a.b", preserved: true},
		{name: "identifiant vide", incoming: "", preserved: false},
		{name: "injection de journal", incoming: "abc\ndef=1", preserved: false},
		{name: "caractères exotiques", incoming: "abc<script>", preserved: false},
		{name: "identifiant démesuré", incoming: strings.Repeat("a", 65), preserved: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var seen string
			srv := newTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = server.RequestIDFromContext(r.Context())
			}), nil)

			do(t, srv, http.MethodGet, "/", map[string]string{server.RequestIDHeader: tc.incoming})

			if tc.preserved && seen != tc.incoming {
				t.Errorf("identifiant = %q, attendu la reprise de %q", seen, tc.incoming)
			}
			if !tc.preserved && seen == tc.incoming {
				t.Errorf("identifiant = %q, l'entrée du client devait être écartée", seen)
			}
			if seen == "" {
				t.Error("une requête doit toujours finir avec un identifiant")
			}
		})
	}
}

func TestRequestIDFromContextOutsideRequest(t *testing.T) {
	t.Parallel()

	if id := server.RequestIDFromContext(t.Context()); id != "" {
		t.Errorf("identifiant = %q, attendu une chaîne vide hors requête", id)
	}
}

// TestRecoverTurnsPanicIntoError vérifie qu'une panique de gestionnaire coûte
// une requête, pas le processus, et que la pile ne part pas au client.
func TestRecoverTurnsPanicIntoError(t *testing.T) {
	t.Parallel()

	var journal bytes.Buffer
	srv, err := server.New(server.Options{
		Config: testConfig(),
		Logger: slog.New(slog.NewJSONHandler(&journal, nil)),
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("le mur porteur a cédé")
		}),
	})
	if err != nil {
		t.Fatalf("server.New() a échoué : %v", err)
	}

	resp := do(t, srv, http.MethodGet, "/", nil)
	if resp.Status != http.StatusInternalServerError {
		t.Errorf("statut = %d, attendu 500", resp.Status)
	}
	if strings.Contains(resp.Body, "mur porteur") || strings.Contains(resp.Body, "goroutine") {
		t.Errorf("corps = %q, ne doit contenir ni le message de panique ni la pile", resp.Body)
	}
	if !strings.Contains(journal.String(), "mur porteur") {
		t.Errorf("journal = %q, doit conserver la trace de la panique", journal.String())
	}
}

// TestAccessLogRecordsRequest vérifie que chaque requête laisse une ligne
// exploitable, corrélée par l'identifiant de requête.
func TestAccessLogRecordsRequest(t *testing.T) {
	t.Parallel()

	var journal bytes.Buffer
	srv, err := server.New(server.Options{
		Config: testConfig(),
		Logger: slog.New(slog.NewJSONHandler(&journal, nil)),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			if _, err := w.Write([]byte("créé")); err != nil {
				t.Errorf("écriture de la réponse : %v", err)
			}
		}),
	})
	if err != nil {
		t.Fatalf("server.New() a échoué : %v", err)
	}

	resp := do(t, srv, http.MethodPost, "/devis", nil)

	line := lastAccessLine(t, &journal)
	if line["method"] != http.MethodPost {
		t.Errorf("method = %v", line["method"])
	}
	if line["path"] != "/devis" {
		t.Errorf("path = %v", line["path"])
	}
	if line["status"] != float64(http.StatusCreated) {
		t.Errorf("status = %v, attendu 201", line["status"])
	}
	if line["bytes"] != float64(len("créé")) {
		t.Errorf("bytes = %v, attendu %d", line["bytes"], len("créé"))
	}
	if line["request_id"] != resp.Header.Get(server.RequestIDHeader) {
		t.Errorf("request_id = %v, doit correspondre à l'en-tête renvoyé", line["request_id"])
	}
}

// TestAccessLogLevelFollowsStatus : une avalanche de 500 doit se distinguer
// d'une avalanche de 200 sans avoir à relire les codes un par un.
func TestAccessLogLevelFollowsStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		want   string
	}{
		{name: "succès", status: http.StatusOK, want: "INFO"},
		{name: "faute du client", status: http.StatusNotFound, want: "WARN"},
		{name: "faute du serveur", status: http.StatusInternalServerError, want: "ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var journal bytes.Buffer
			srv, err := server.New(server.Options{
				Config: testConfig(),
				Logger: slog.New(slog.NewJSONHandler(&journal, nil)),
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
				}),
			})
			if err != nil {
				t.Fatalf("server.New() a échoué : %v", err)
			}

			do(t, srv, http.MethodGet, "/", nil)

			if got := lastAccessLine(t, &journal)["level"]; got != tc.want {
				t.Errorf("level = %v, attendu %v", got, tc.want)
			}
		})
	}
}

// lastAccessLine extrait la dernière ligne de journal, celle que le journal
// d'accès écrit une fois la réponse partie.
func lastAccessLine(t *testing.T, journal *bytes.Buffer) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(journal.String()), "\n")
	last := lines[len(lines)-1]

	var record map[string]any
	if err := json.Unmarshal([]byte(last), &record); err != nil {
		t.Fatalf("ligne de journal illisible %q : %v", last, err)
	}

	return record
}

// TestServeFinishesInFlightRequests est le test qui compte pour l'arrêt
// gracieux : une requête déjà commencée au moment du signal doit aboutir, pas
// être coupée au milieu de sa réponse.
func TestServeFinishesInFlightRequests(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})

	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		if _, err := w.Write([]byte("terminé")); err != nil {
			t.Errorf("écriture de la réponse : %v", err)
		}
	}), nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	listener := listen(t, ctx)

	var wg sync.WaitGroup
	var serveErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		serveErr = srv.Serve(ctx, listener)
	}()

	type result struct {
		body string
		err  error
	}
	responses := make(chan result, 1)

	url := "http://" + listener.Addr().String() + "/"
	go func() {
		// Le contexte de la requête survit à l'annulation du serveur : c'est
		// exactement ce qu'on veut mesurer, la réponse doit arriver quand même.
		body, err := fetch(context.WithoutCancel(ctx), url)
		responses <- result{body: body, err: err}
	}()

	<-entered
	// Le signal d'arrêt tombe pendant que le gestionnaire travaille.
	cancel()
	close(release)

	select {
	case got := <-responses:
		if got.err != nil {
			t.Fatalf("la requête en cours a été coupée : %v", got.err)
		}
		if got.body != "terminé" {
			t.Errorf("corps = %q, attendu \"terminé\"", got.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("la requête en cours n'a jamais abouti")
	}

	wg.Wait()
	if serveErr != nil {
		t.Errorf("Serve() = %v, un arrêt demandé n'est pas une erreur", serveErr)
	}
}

// TestServeRefusesNewConnectionsAfterShutdown complète le test précédent :
// l'arrêt gracieux termine ce qui est commencé, mais n'accepte plus rien.
func TestServeRefusesNewConnectionsAfterShutdown(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil)

	ctx, cancel := context.WithCancel(t.Context())

	listener := listen(t, ctx)
	url := "http://" + listener.Addr().String() + "/"

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, listener) }()

	if _, err := fetch(t.Context(), url); err != nil {
		t.Fatalf("première requête : %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve() = %v", err)
	}

	if _, err := fetch(t.Context(), url); err == nil {
		t.Error("le serveur arrêté ne doit plus accepter de connexion")
	}
}
