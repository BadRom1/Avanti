// Package db ouvre le pool de connexions PostgreSQL et vérifie qu'il joint bien
// la base.
//
// C'est le seul endroit du socle qui connaisse pgx. Les domaines n'en voient
// rien : ils déclarent des ports que les adapters de persistance implémentent
// avec ce pool (R1 de docs/ARCHITECTURE.md).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// retryInterval sépare deux tentatives de premier contact. Un Postgres lancé
// par docker compose accepte les connexions TCP avant d'être prêt à répondre :
// réessayer quelques fois évite un échec au démarrage qui n'aurait duré qu'une
// seconde.
const retryInterval = 250 * time.Millisecond

// Open construit le pool décrit par dsn et attend d'obtenir une réponse de la
// base, sans dépasser timeout. Le pool renvoyé est à fermer par l'appelant.
func Open(ctx context.Context, logger *slog.Logger, dsn string, timeout time.Duration) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("chaîne de connexion PostgreSQL invalide : %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("création du pool PostgreSQL : %w", err)
	}

	if err := waitReady(ctx, logger, pool, timeout); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}

// waitReady répète le ping jusqu'à ce que la base réponde ou que timeout expire.
func waitReady(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, timeout time.Duration) error {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for attempt := 1; ; attempt++ {
		lastErr = pool.Ping(deadline)
		if lastErr == nil {
			return nil
		}

		logger.Debug("PostgreSQL ne répond pas encore, nouvelle tentative",
			slog.Int("attempt", attempt),
			slog.String("error", lastErr.Error()))

		select {
		case <-deadline.Done():
			return fmt.Errorf("PostgreSQL injoignable après %s : %w", timeout, errors.Join(lastErr, deadline.Err()))
		case <-time.After(retryInterval):
		}
	}
}

// Ping vérifie que la base répond encore. C'est la sonde de disponibilité du
// serveur HTTP : elle doit rester bon marché et bornée par le contexte reçu.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL : %w", err)
	}
	return nil
}

// StdlibDB expose le pool sous la forme *sql.DB attendue par les outils qui ne
// parlent que database/sql — goose, en l'occurrence.
//
// Le *sql.DB renvoyé emprunte les connexions du pool : le fermer libère cet
// emprunt et laisse le pool intact, qui reste la propriété de l'appelant d'Open.
func StdlibDB(pool *pgxpool.Pool) *sql.DB {
	return stdlib.OpenDBFromPool(pool)
}
