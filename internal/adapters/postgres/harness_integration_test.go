// Tests d'intégration de l'adapter PostgreSQL : ils exercent le SQL réel, parce
// que c'est le seul moyen de vérifier du SQL. Un dépôt en mémoire dirait que le
// code compile, pas que la requête est juste, ni que les contraintes de la table
// mordent.
//
// Deux façons d'obtenir une base, dans cet ordre — les mêmes que pour le socle :
//
//  1. AVANTI_TEST_DATABASE_URL, si la variable est renseignée. C'est le chemin de
//     la CI, où un service PostgreSQL tourne déjà à côté du job.
//  2. testcontainers, sinon. C'est le chemin du poste de développement : rien à
//     préparer, le conteneur naît et meurt avec la suite.
//
// Sans l'une ni l'autre, les tests se sautent proprement : un contributeur sans
// Docker doit pouvoir lancer `make test`.
package postgres_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/platform/db"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
	"github.com/Romain-Badino/Avanti/internal/platform/migrate"
)

// testDatabaseURLEnv désigne un PostgreSQL déjà en place, celui de la CI.
const testDatabaseURLEnv = "AVANTI_TEST_DATABASE_URL"

// postgresImage suit la version du docker-compose de développement et du service
// PostgreSQL de la CI, pour que le SQL soit vérifié contre la même version
// partout.
const postgresImage = "postgres:18-alpine"

const containerStartTimeout = 3 * time.Minute

var (
	// serverDSN pointe le serveur sur lequel chaque test crée sa propre base.
	serverDSN string
	// skipReason explique pourquoi les tests se sautent, quand ils se sautent.
	skipReason string
	// dbCounter distingue les bases de deux tests parallèles.
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

// newRepo monte une base neuve, y applique les migrations et rend le dépôt
// prêt à l'emploi. Une base par test : deux tests parallèles ne se marchent pas
// sur les pieds, et un échec laisse un état lisible.
func newRepo(t *testing.T) *postgres.UserRepo {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	repo, err := postgres.NewUserRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewUserRepo() échoué : %v", err)
	}

	return repo
}

// freshDatabase crée une base vierge sur le serveur de test et la supprime au
// nettoyage.
func freshDatabase(t *testing.T) string {
	t.Helper()

	if skipReason != "" {
		t.Skip("test d'intégration sauté : " + skipReason)
	}
	if serverDSN == "" {
		t.Skip("test d'intégration sauté : aucune base PostgreSQL de test disponible")
	}

	name := fmt.Sprintf("avanti_users_%d_%d", os.Getpid(), dbCounter.Add(1))

	admin, err := pgx.Connect(t.Context(), serverDSN)
	if err != nil {
		t.Fatalf("connexion au serveur PostgreSQL de test : %v", err)
	}
	defer func() {
		if closeErr := admin.Close(t.Context()); closeErr != nil {
			t.Errorf("fermeture de la connexion d'administration : %v", closeErr)
		}
	}()

	// Le nom est fabriqué localement à partir d'un compteur : il ne vient d'aucune
	// entrée externe, et PostgreSQL ne permet pas de paramétrer un identifiant de
	// CREATE DATABASE.
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
		defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // la base est jetable, une fermeture ratée n'a rien à apprendre au test.

		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("suppression de la base de test %s : %v", name, err)
		}
	})

	return replaceDatabase(t, serverDSN, name)
}

// replaceDatabase récrit la chaîne de connexion pour viser une autre base du même
// serveur.
func replaceDatabase(t *testing.T, dsn, base string) string {
	t.Helper()

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("analyse de la chaîne de connexion de test : %v", err)
	}

	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		config.User, config.Password, config.Host, config.Port, base)
}

func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	pool, err := db.Open(t.Context(), logging.Discard(), dsn, 30*time.Second)
	if err != nil {
		t.Fatalf("ouverture du pool sur la base de test : %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	sqlDB := db.StdlibDB(pool)
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("fermeture de la vue database/sql : %v", err)
		}
	}()

	if err := migrate.Up(t.Context(), logging.Discard(), sqlDB); err != nil {
		t.Fatalf("migrate.Up() échoué : %v", err)
	}
}

// testAccount fabrique un compte valide et complet, prêt à être inséré. Les
// champs qu'un test veut particuliers, il les écrase après coup.
func testAccount(t *testing.T, email string, role identity.Role) identity.User {
	t.Helper()

	id, err := identity.NewID()
	if err != nil {
		t.Fatalf("identity.NewID() échoué : %v", err)
	}

	instant := time.Date(2026, time.March, 15, 10, 30, 0, 0, time.UTC)

	return identity.User{
		ID:           id,
		Email:        email,
		DisplayName:  "Compte de test",
		PasswordHash: identity.PasswordHash("$argon2id$v=19$m=19456,t=2,p=1$c2VsZGV0ZXN0$ZW1wcmVpbnRlZGV0ZXN0"),
		Role:         role,
		Active:       true,
		CreatedAt:    instant,
		UpdatedAt:    instant,
	}
}
