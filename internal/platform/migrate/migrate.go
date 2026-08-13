// Package migrate embarque le schéma PostgreSQL d'Avanti et l'applique.
//
// Les fichiers SQL sont compilés dans le binaire : déployer Avanti, c'est
// copier un exécutable, pas un exécutable *et* un répertoire de migrations qu'on
// oublierait de mettre à jour. Le corollaire est une règle stricte — une
// migration publiée ne se modifie plus, on en ajoute une autre.
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/pressly/goose/v3"
)

// migrationsFS porte les fichiers SQL. Ils vivent dans un sous-répertoire pour
// que le reste du package ne se retrouve pas embarqué avec eux.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// FS renvoie le système de fichiers des migrations, racine au niveau des
// fichiers SQL. Les tests d'intégration s'en servent pour compter ce qui doit
// être appliqué.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("lecture des migrations embarquées : %w", err)
	}
	return sub, nil
}

// Up applique les migrations manquantes et journalise celles qui ont tourné.
//
// L'opération est idempotente : une base déjà à jour ne subit rien et Up
// n'écrit rien. C'est ce qui autorise à l'appeler à chaque démarrage.
func Up(ctx context.Context, logger *slog.Logger, database *sql.DB) error {
	migrations, err := FS()
	if err != nil {
		return err
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations)
	if err != nil {
		return fmt.Errorf("préparation des migrations : %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("application des migrations : %w", err)
	}

	if len(results) == 0 {
		logger.Info("schéma de base déjà à jour")
		return nil
	}

	for _, result := range results {
		logger.Info("migration appliquée",
			slog.Int64("version", result.Source.Version),
			slog.String("source", result.Source.Path),
			slog.Duration("duration", result.Duration))
	}
	logger.Info("schéma de base mis à jour", slog.Int("applied", len(results)))

	return nil
}
