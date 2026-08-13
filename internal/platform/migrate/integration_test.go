// Tests d'intégration du socle : ils exercent les migrations et la sonde de
// disponibilité contre un PostgreSQL réel, parce que c'est le seul moyen de
// vérifier du SQL.
//
// Deux façons d'obtenir cette base, dans cet ordre :
//
//  1. AVANTI_TEST_DATABASE_URL, si la variable est renseignée. C'est le chemin
//     de la CI, où un service PostgreSQL tourne déjà à côté du job — démarrer un
//     conteneur depuis un conteneur ne rendrait service à personne.
//  2. testcontainers, sinon. C'est le chemin du poste de développement : rien à
//     préparer, le conteneur naît et meurt avec la suite.
//
// Si aucune des deux n'est disponible, les tests se sautent proprement plutôt
// que d'échouer : un contributeur sans Docker doit pouvoir lancer `make test`.
package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Romain-Badino/Avanti/internal/platform/config"
	"github.com/Romain-Badino/Avanti/internal/platform/db"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
	"github.com/Romain-Badino/Avanti/internal/platform/migrate"
	"github.com/Romain-Badino/Avanti/internal/platform/server"
)

// testDatabaseURLEnv nomme la base de test fournie par l'environnement.
const testDatabaseURLEnv = "AVANTI_TEST_DATABASE_URL"

// postgresImage est l'image utilisée par testcontainers. Elle suit celle du
// docker-compose de développement et du service PostgreSQL de la CI : tester
// contre une version qu'on ne fera jamais tourner n'apporterait rien.
const postgresImage = "postgres:18-alpine"

// containerStartTimeout borne le démarrage du conteneur, téléchargement de
// l'image compris au premier lancement.
const containerStartTimeout = 3 * time.Minute

// serverDSN est le point de départ commun aux tests du paquet : une base
// administrable dans laquelle chaque test se taille sa propre base.
var (
	serverDSN  string
	skipReason string
	// dbCounter distingue les bases créées par les tests, qui tournent en
	// parallèle.
	dbCounter atomic.Int64
)

func TestMain(m *testing.M) {
	code := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
		defer cancel()

		if dsn := strings.TrimSpace(os.Getenv(testDatabaseURLEnv)); dsn != "" {
			serverDSN = dsn
			return m.Run()
		}

		container, err := startPostgres(ctx)
		if err != nil {
			skipReason = err.Error()
			return m.Run()
		}
		defer func() {
			if terminateErr := testcontainers.TerminateContainer(container); terminateErr != nil {
				fmt.Fprintf(os.Stderr, "arrêt du conteneur PostgreSQL : %v\n", terminateErr)
			}
		}()

		serverDSN, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			skipReason = fmt.Sprintf("chaîne de connexion du conteneur indisponible : %v", err)
		}

		return m.Run()
	}()

	os.Exit(code)
}

// startPostgres lève un PostgreSQL jetable, après avoir vérifié que Docker
// répond — sans quoi l'échec serait un long délai d'attente plutôt qu'un saut
// immédiat.
func startPostgres(ctx context.Context) (*tcpostgres.PostgresContainer, error) {
	provider, providerErr := testcontainers.NewDockerProvider()
	if providerErr != nil {
		return nil, fmt.Errorf("Docker indisponible et %s non renseignée : %w", testDatabaseURLEnv, providerErr)
	}
	defer func() {
		if closeErr := provider.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "fermeture du client Docker : %v\n", closeErr)
		}
	}()

	if healthErr := provider.Health(ctx); healthErr != nil {
		return nil, fmt.Errorf("Docker ne répond pas et %s non renseignée : %w", testDatabaseURLEnv, healthErr)
	}

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("avanti_test"),
		tcpostgres.WithUsername("avanti"),
		tcpostgres.WithPassword("change-me-test-only"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("démarrage du conteneur PostgreSQL : %w", err)
	}

	return container, nil
}

// freshDatabase taille une base neuve pour le test appelant et la rend à la fin.
//
// L'isolation est indispensable : « les migrations s'appliquent sur une base
// vierge » n'est vérifiable que sur une base réellement vierge, et la CI
// réutilise le même serveur d'un test à l'autre.
func freshDatabase(t *testing.T) string {
	t.Helper()

	if skipReason != "" {
		t.Skip("test d'intégration sauté : " + skipReason)
	}
	if serverDSN == "" {
		t.Skip("test d'intégration sauté : aucune base PostgreSQL de test disponible")
	}

	name := fmt.Sprintf("avanti_it_%d_%d", os.Getpid(), dbCounter.Add(1))

	admin, err := pgx.Connect(t.Context(), serverDSN)
	if err != nil {
		t.Fatalf("connexion au serveur PostgreSQL de test : %v", err)
	}
	defer func() {
		if err := admin.Close(t.Context()); err != nil {
			t.Errorf("fermeture de la connexion d'administration : %v", err)
		}
	}()

	// Le nom est fabriqué localement à partir d'un compteur : il ne vient
	// d'aucune entrée externe, et PostgreSQL ne permet pas de paramétrer un
	// identifiant de CREATE DATABASE.
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("création de la base de test %s : %v", name, err)
	}

	t.Cleanup(func() {
		// Contexte neuf : celui du test est déjà annulé quand le nettoyage court.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		conn, err := pgx.Connect(ctx, serverDSN)
		if err != nil {
			t.Errorf("reconnexion pour supprimer %s : %v", name, err)
			return
		}
		defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // La base est jetable, une fermeture ratée n'a rien à apprendre au test.

		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("suppression de la base de test %s : %v", name, err)
		}
	})

	return replaceDatabase(t, serverDSN, name)
}

// replaceDatabase réécrit le nom de base d'une chaîne de connexion.
func replaceDatabase(t *testing.T, dsn, name string) string {
	t.Helper()

	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("chaîne de connexion de test illisible : %v", err)
	}

	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		parsed.User, parsed.Password, parsed.Host, parsed.Port, name)
}

// openPool ouvre un pool sur dsn et le referme à la fin du test.
func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	pool, err := db.Open(t.Context(), logging.Discard(), dsn, 30*time.Second)
	if err != nil {
		t.Fatalf("ouverture du pool sur la base de test : %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// applyMigrations rejoue migrate.Up sur le pool fourni.
func applyMigrations(t *testing.T, pool *pgxpool.Pool) error {
	t.Helper()

	sqlDB := db.StdlibDB(pool)
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("fermeture de la vue database/sql : %v", err)
		}
	}()

	return migrate.Up(t.Context(), logging.Discard(), sqlDB)
}

// TestUpAppliesSchemaToEmptyDatabase est le test de fond des migrations : sur une
// base vierge, le schéma attendu existe après coup.
func TestUpAppliesSchemaToEmptyDatabase(t *testing.T) {
	t.Parallel()

	pool := openPool(t, freshDatabase(t))

	if err := applyMigrations(t, pool); err != nil {
		t.Fatalf("migrate.Up() a échoué : %v", err)
	}

	var baseline string
	err := pool.QueryRow(t.Context(),
		"SELECT value FROM app_info WHERE key = $1", "schema_baseline").Scan(&baseline)
	if err != nil {
		t.Fatalf("lecture de app_info : %v", err)
	}
	if baseline != "00001_app_info" {
		t.Errorf("schema_baseline = %q", baseline)
	}

	var applied int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id > 0").Scan(&applied); err != nil {
		t.Fatalf("lecture de goose_db_version : %v", err)
	}
	if applied != countEmbeddedMigrations(t) {
		t.Errorf("%d migrations enregistrées, %d embarquées", applied, countEmbeddedMigrations(t))
	}
}

// TestUpIsIdempotent : les migrations tournent à chaque démarrage. Si un second
// passage n'était pas neutre, redémarrer l'application deviendrait risqué.
func TestUpIsIdempotent(t *testing.T) {
	t.Parallel()

	pool := openPool(t, freshDatabase(t))

	if err := applyMigrations(t, pool); err != nil {
		t.Fatalf("premier passage de migrate.Up() : %v", err)
	}
	if err := applyMigrations(t, pool); err != nil {
		t.Fatalf("second passage de migrate.Up() : %v", err)
	}

	var rows int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM app_info").Scan(&rows); err != nil {
		t.Fatalf("lecture de app_info : %v", err)
	}
	if rows != 2 {
		t.Errorf("app_info contient %d lignes après deux passages, attendu 2", rows)
	}
}

// countEmbeddedMigrations compte les fichiers SQL embarqués, pour que le test
// n'ait pas à répéter un nombre qui changera à chaque lot.
func countEmbeddedMigrations(t *testing.T) int {
	t.Helper()

	migrations, err := migrate.FS()
	if err != nil {
		t.Fatalf("migrate.FS() a échoué : %v", err)
	}

	entries, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		t.Fatalf("énumération des migrations : %v", err)
	}

	return len(entries)
}

// TestReadyzReflectsDatabaseState boucle la boucle : la sonde branchée sur un
// vrai pool répond 200, et 503 dès que la base n'est plus joignable.
func TestReadyzReflectsDatabaseState(t *testing.T) {
	t.Parallel()

	pool := openPool(t, freshDatabase(t))

	srv, err := server.New(server.Options{
		Config: &config.Config{ListenAddr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second},
		Logger: logging.Discard(),
		Ready:  func(ctx context.Context) error { return db.Ping(ctx, pool) },
	})
	if err != nil {
		t.Fatalf("server.New() a échoué : %v", err)
	}

	if status, body := probe(t, srv); status != http.StatusOK {
		t.Errorf("/readyz = %d (%q) sur une base joignable, attendu 200", status, body)
	}

	// Le pool fermé simule la perte de la base sans avoir à arrêter le serveur
	// PostgreSQL, qui est partagé avec les autres tests.
	pool.Close()

	if status, _ := probe(t, srv); status != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d après perte de la base, attendu 503", status)
	}
}

// probe interroge /readyz sur la chaîne complète du serveur.
func probe(t *testing.T, srv *server.Server) (status int, body string) {
	t.Helper()

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/readyz", http.NoBody))

	resp := rec.Result()
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("fermeture du corps de réponse : %v", closeErr)
		}
	}()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	return resp.StatusCode, string(payload)
}

// TestOpenRejectsUnreachableDatabase vérifie que le démarrage échoue vite et
// clairement quand la base est absente, au lieu d'attendre indéfiniment.
func TestOpenRejectsUnreachableDatabase(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, err := db.Open(t.Context(), logging.Discard(),
		"postgres://avanti:change-me@127.0.0.1:1/avanti?sslmode=disable", 2*time.Second)

	if err == nil {
		t.Fatal("db.Open() doit échouer sur une base injoignable")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("db.Open() a mis %s à renoncer, le délai n'est pas respecté", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("erreur = %v, doit mentionner le dépassement du délai", err)
	}
}
