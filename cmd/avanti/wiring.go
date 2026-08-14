package main

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/adapters/export"
	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/adapters/storage"
	"github.com/Romain-Badino/Avanti/internal/adapters/web"
	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/config"
	"github.com/Romain-Badino/Avanti/internal/platform/db"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
	"github.com/Romain-Badino/Avanti/internal/platform/migrate"
)

// instance rassemble ce que toute sous-commande a besoin d'ouvrir : la
// configuration, le journal, la base, et les services de domaine branchés sur
// elle.
//
// C'est ici que se fait le choix des implémentations concrètes des ports — le
// travail propre à cmd/avanti, seul endroit du dépôt autorisé à connaître les
// domaines et les adapters à la fois (R4 de docs/ARCHITECTURE.md).
type instance struct {
	cfg    *config.Config
	logger *slog.Logger
	build  platform.BuildInfo
	pool   *pgxpool.Pool

	// accounts est le service du domaine identity, monté sur le dépôt PostgreSQL et
	// le hacheur argon2id.
	accounts *identity.AccountService

	// devisService porte les cas d'usage de la consultation des artisans, montés
	// sur leur dépôt PostgreSQL. L'adapter web le recevra tel quel : c'est le
	// domaine qu'il voit, jamais sa persistance (R4).
	devisService *devis.Service

	// documentsService porte les cas d'usage des pièces du dossier, montés sur
	// le dépôt PostgreSQL des métadonnées et sur le stockage de contenu que la
	// configuration a choisi — voir newDocumentStorage.
	documentsService *document.Service

	// financeService porte les cas d'usage de l'argent du chantier, montés sur
	// leur dépôt PostgreSQL.
	financeService *finance.Service

	// oauthStore est le magasin du serveur d'autorisation. Il vit dans la famille
	// postgres et sera injecté dans l'adapter web : c'est le point où les deux
	// familles se rencontrent, et le seul endroit du dépôt où c'est permis (R4 de
	// docs/ARCHITECTURE.md).
	oauthStore *postgres.OAuthStore
}

// openInstance monte tout ce qui précède, dans l'ordre de ses dépendances.
//
// La fonction de fermeture rendue est à appeler dans tous les cas, y compris sur
// erreur du reste de la commande : c'est elle qui rend le pool de connexions.
func openInstance(ctx context.Context, stderr io.Writer) (*instance, func(), error) {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return nil, func() {}, err
	}

	// Les journaux vont sur la sortie d'erreur : la sortie standard reste
	// disponible pour ce qu'une commande produit réellement.
	logger := logging.New(stderr, cfg)

	pool, err := db.Open(ctx, logger, cfg.DatabaseURL, cfg.DBConnectTimeout)
	if err != nil {
		return nil, func() {}, err
	}

	if schemaErr := applySchema(ctx, cfg, logger, pool); schemaErr != nil {
		pool.Close()
		return nil, func() {}, schemaErr
	}

	repo, err := postgres.NewUserRepo(pool)
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	accounts, err := identity.NewAccountService(identity.ServiceOptions{
		Repo: repo,
		// Le hacheur de production, celui dont la lenteur est la fonction. Sa
		// construction est le seul endroit du dépôt qui le nomme : partout ailleurs,
		// c'est le port identity.Hasher qui circule.
		Hasher: identity.NewArgon2idHasher(),
	})
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	oauthStore, err := postgres.NewOAuthStore(pool)
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	devisService, err := newDevisService(pool)
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	documentsService, err := newDocumentService(cfg, pool)
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	financeService, err := newFinanceService(pool)
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	return &instance{
		cfg:              cfg,
		logger:           logger,
		build:            platform.Build(),
		pool:             pool,
		accounts:         accounts,
		devisService:     devisService,
		documentsService: documentsService,
		financeService:   financeService,
		oauthStore:       oauthStore,
	}, pool.Close, nil
}

// newDevisService branche le domaine des devis sur son dépôt PostgreSQL.
//
// L'horloge et le générateur d'identifiants restent ceux par défaut : ce sont
// des dépendances que le domaine expose pour ses tests, pas des réglages
// d'exploitation.
func newDevisService(pool *pgxpool.Pool) (*devis.Service, error) {
	repo, err := postgres.NewDevisRepo(pool)
	if err != nil {
		return nil, err
	}

	return devis.NewService(devis.ServiceOptions{Repo: repo})
}

// newDocumentService branche le domaine des documents sur ses deux ports : le
// dépôt PostgreSQL des métadonnées, et le stockage de contenu choisi par la
// configuration.
func newDocumentService(cfg *config.Config, pool *pgxpool.Pool) (*document.Service, error) {
	repo, err := postgres.NewDocumentRepo(pool)
	if err != nil {
		return nil, err
	}

	contentStorage, err := newDocumentStorage(cfg)
	if err != nil {
		return nil, err
	}

	return document.NewService(document.ServiceOptions{Repo: repo, Storage: contentStorage})
}

// newFinanceService branche le domaine des finances sur son dépôt PostgreSQL.
func newFinanceService(pool *pgxpool.Pool) (*finance.Service, error) {
	repo, err := postgres.NewFinanceRepo(pool)
	if err != nil {
		return nil, err
	}

	return finance.NewService(finance.ServiceOptions{Repo: repo})
}

// newExports construit les formats du dossier d'assurance, indexés par le
// segment d'URL qui les désigne.
//
// C'est le modèle d'extension de docs/ARCHITECTURE.md §3, second point
// d'extension officiel : le port finance.ExportFormat est l'interface, ses
// implémentations vivent dans adapters/export, et c'est ici — dans cmd/avanti,
// seul autorisé à connaître les deux (R4) — qu'elles se branchent. Un
// troisième format s'ajouterait par une implémentation de plus et une entrée
// de plus dans cette map, sans toucher au domaine ni à l'adapter web.
func newExports() map[string]finance.ExportFormat {
	return map[string]finance.ExportFormat{
		"csv": export.NewCSV(),
		"pdf": export.NewPDF(),
	}
}

// newDocumentStorage choisit l'implémentation du port document.Storage selon
// la configuration.
//
// C'est le modèle d'extension de docs/ARCHITECTURE.md §3 en un endroit : le
// port du domaine est le point d'extension, ses implémentations vivent dans
// adapters/storage, et c'est ici — dans cmd/avanti, seul autorisé à connaître
// les deux (R4) — que la configuration tranche laquelle brancher. Un troisième
// stockage s'ajouterait par une implémentation de plus et un cas de plus dans
// ce switch, sans toucher au domaine ni à l'adapter web.
func newDocumentStorage(cfg *config.Config) (document.Storage, error) {
	switch cfg.StorageBackend {
	case config.StorageS3:
		return storage.NewS3(storage.S3Options{
			Endpoint:  cfg.S3Endpoint,
			Bucket:    cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Region:    cfg.S3Region,
			UseSSL:    cfg.S3UseSSL,
		})
	default:
		// La configuration a déjà validé la valeur : le défaut ne peut être
		// que filesystem.
		return storage.NewFilesystem(cfg.DocumentsDir)
	}
}

// oauthPurgeTimeout borne une passe de ménage. Elle est courte : la requête est
// un DELETE indexé sur une table qui compte quelques milliers de lignes, et une
// passe qui traînerait vaut mieux abandonnée — la suivante arrive dans l'heure.
const oauthPurgeTimeout = 30 * time.Second

// startOAuthPurge lance le ménage périodique des codes et jetons expirés, et
// rend de quoi l'arrêter.
//
// Le ménage vit ici plutôt que dans l'adapter web parce que c'est cmd/avanti qui
// décide de la vie du processus (R3 de docs/ARCHITECTURE.md) : lancer une
// goroutine perpétuelle depuis une bibliothèque la rendrait impossible à arrêter
// pour son appelant. C'est le même partage que pour le nettoyage des sessions,
// dont pgxstore reçoit la période sans choisir quand elle commence.
//
// La fonction rendue est bloquante jusqu'à l'arrêt effectif : sans cela, le
// processus pourrait fermer son pool de connexions pendant qu'une passe tourne
// encore, et le journal se terminerait sur une erreur qui n'en est pas une.
func startOAuthPurge(ctx context.Context, app *instance) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(web.OAuthPurgeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				purgeOAuth(ctx, app)
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// purgeOAuth exécute une passe de ménage.
//
// Un échec se journalise et ne remonte pas : ne pas avoir fait le ménage n'est
// pas une raison d'arrêter de servir, et la passe suivante retentera.
func purgeOAuth(ctx context.Context, app *instance) {
	ctx, cancel := context.WithTimeout(ctx, oauthPurgeTimeout)
	defer cancel()

	removed, err := app.oauthStore.PurgeExpired(ctx, time.Now())
	if err != nil {
		app.logger.WarnContext(ctx, "purge des jetons OAuth expirés",
			slog.String("error", err.Error()))
		return
	}
	if removed > 0 {
		app.logger.InfoContext(ctx, "jetons OAuth expirés supprimés",
			slog.Int64("count", removed))
	}
}

// applySchema rejoue les migrations manquantes, si la configuration le
// permet.
//
// Les sous-commandes de gestion des comptes passent par ici comme `serve` : c'est
// ainsi que `avanti user add` fonctionne sur une base fraîchement créée, avant
// que le serveur n'ait jamais tourné — l'ordre naturel d'une première
// installation.
func applySchema(ctx context.Context, cfg *config.Config, logger *slog.Logger, pool *pgxpool.Pool) error {
	if !cfg.MigrateOnStart {
		logger.Warn("migrations désactivées, le schéma doit déjà être à jour")
		return nil
	}

	// goose ne parle que database/sql : on emprunte une vue sur le pool le temps
	// de la migration, puis on la rend — le pool reste ouvert.
	sqlDB := db.StdlibDB(pool)
	migrateErr := migrate.Up(ctx, logger, sqlDB)
	if closeErr := sqlDB.Close(); closeErr != nil {
		logger.Warn("fermeture de la vue database/sql du pool",
			slog.String("error", closeErr.Error()))
	}

	return migrateErr
}
