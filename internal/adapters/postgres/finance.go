package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// FinanceRepo implémente [finance.Repository] sur PostgreSQL.
//
// Les colonnes portent le vocabulaire du domaine (entreprise, montant,
// statut_paiement, statut_assurance, moyen) : c'est le même modèle vu des deux
// côtés, et la correspondance se lit sans table de traduction.
type FinanceRepo struct {
	pool *pgxpool.Pool
}

// NewFinanceRepo construit le dépôt sur un pool de connexions existant. Le pool
// reste la propriété de l'appelant — cmd/avanti, qui l'ouvre et le ferme.
func NewFinanceRepo(pool *pgxpool.Pool) (*FinanceRepo, error) {
	if pool == nil {
		return nil, errors.New("postgres : pool de connexions manquant")
	}
	return &FinanceRepo{pool: pool}, nil
}

// factureColumns est la liste de sélection commune aux lectures de factures. La
// factoriser garantit que les colonnes arrivent dans l'ordre qu'attend
// [scanFacture].
const factureColumns = `id, devis_id, entreprise, montant, date_piece, numero, notes, ` +
	`statut_paiement, payee_le, statut_assurance, envoyee_le, montant_rembourse, rembourse_le, ` +
	`saisi_par, cree_le, modifie_le`

// acompteColumns est la liste de sélection commune aux lectures d'acomptes.
const acompteColumns = `id, devis_id, entreprise, montant, date_piece, moyen, notes, ` +
	`statut_assurance, envoyee_le, montant_rembourse, rembourse_le, saisi_par, cree_le, modifie_le`

// CreateFacture insère une facture.
func (f *FinanceRepo) CreateFacture(ctx context.Context, facture finance.Facture) error {
	id, auteur, err := financeWriteIDs(facture.ID.String(), facture.RecordedBy.String(), "facture")
	if err != nil {
		return err
	}

	const query = `
		INSERT INTO factures (` + factureColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

	if _, err := f.pool.Exec(ctx, query,
		id, facture.DevisID, facture.Entreprise, int64(facture.Montant), facture.Date,
		facture.Numero, facture.Notes, facture.Paiement.String(), optionalTime(facture.PaidAt),
		facture.Assurance.Statut.String(), optionalTime(facture.Assurance.SentAt),
		int64(facture.Assurance.MontantRembourse), optionalTime(facture.Assurance.RefundedAt),
		auteur, facture.CreatedAt, facture.UpdatedAt); err != nil {
		return fmt.Errorf("insertion de la facture %s : %w", facture.ID, err)
	}

	return nil
}

// FactureByID lit une facture par son identifiant.
func (f *FinanceRepo) FactureByID(ctx context.Context, factureID finance.ID) (finance.Facture, error) {
	id, err := lookupUUID(factureID.String(), finance.ErrUnknownFacture)
	if err != nil {
		return finance.Facture{}, err
	}

	const query = `SELECT ` + factureColumns + ` FROM factures WHERE id = $1`

	facture, err := scanFacture(f.pool.QueryRow(ctx, query, id))
	if err != nil {
		return finance.Facture{}, fmt.Errorf("lecture de la facture %s : %w", factureID, err)
	}

	return facture, nil
}

// ListFactures renvoie toutes les factures, de la plus récente à la plus
// ancienne.
//
// Sans pagination : même raisonnement que pour les demandes de devis — un
// chantier compte quelques dizaines de factures, et le jour où ce ne serait
// plus vrai, c'est le port du domaine qu'il faudrait revoir, pas seulement
// cette requête. L'identifiant fige l'ordre des saisies simultanées.
func (f *FinanceRepo) ListFactures(ctx context.Context) ([]finance.Facture, error) {
	const query = `SELECT ` + factureColumns + ` FROM factures ORDER BY date_piece DESC, cree_le DESC, id`

	factures, err := queryAll(ctx, f.pool, scanFacture, query)
	if err != nil {
		return nil, fmt.Errorf("lecture des factures : %w", err)
	}

	return factures, nil
}

// UpdateFacture réécrit une facture entière, sous garde optimiste.
//
// C'est le modèle simple assumé par le port : l'entité relue et transformée
// par le domaine fait foi, la ligne est remplacée d'un bloc — modifie_le
// compris — et les contraintes CHECK de la table gardent les états
// incohérents. La garde est la clause `modifie_le = expected` : une écriture
// concurrente qui a changé la ligne entre la lecture et cette réécriture rend
// la clause fausse, et l'UPDATE ne touche rien — le perdant n'écrase pas ce
// que le gagnant a posé. Aucune ligne touchée se départage alors en relisant :
// facture disparue → inconnue (un 404), facture encore là →
// [finance.ErrConcurrentUpdate] (une course, la page se relit).
func (f *FinanceRepo) UpdateFacture(ctx context.Context, facture finance.Facture, expected time.Time) error {
	id, err := writeUUID(facture.ID.String(), "facture")
	if err != nil {
		return err
	}

	const query = `
		UPDATE factures
		   SET devis_id = $2, entreprise = $3, montant = $4, date_piece = $5,
		       numero = $6, notes = $7, statut_paiement = $8, payee_le = $9,
		       statut_assurance = $10, envoyee_le = $11, montant_rembourse = $12,
		       rembourse_le = $13, modifie_le = $14
		 WHERE id = $1 AND modifie_le = $15`

	tag, err := f.pool.Exec(ctx, query,
		id, facture.DevisID, facture.Entreprise, int64(facture.Montant), facture.Date,
		facture.Numero, facture.Notes, facture.Paiement.String(), optionalTime(facture.PaidAt),
		facture.Assurance.Statut.String(), optionalTime(facture.Assurance.SentAt),
		int64(facture.Assurance.MontantRembourse), optionalTime(facture.Assurance.RefundedAt),
		facture.UpdatedAt, expected)
	if err != nil {
		return fmt.Errorf("réécriture de la facture %s : %w", facture.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return f.explainMissedFactureUpdate(ctx, facture.ID)
	}

	return nil
}

// explainMissedFactureUpdate dit pourquoi une réécriture n'a touché aucune
// ligne — même partage que pour les décisions de devis : une pièce inconnue
// est une URL erronée, une pièce modifiée entre-temps est une course entre
// deux personnes, et l'UPDATE ne les distingue pas. Une lecture de plus, sur
// le seul chemin d'échec, achète cette distinction.
func (f *FinanceRepo) explainMissedFactureUpdate(ctx context.Context, id finance.ID) error {
	if _, err := f.FactureByID(ctx, id); err != nil {
		return err
	}

	return fmt.Errorf("%w : facture %s", finance.ErrConcurrentUpdate, id)
}

// CreateAcompte insère un acompte, sous l'invariant du montant engagé.
//
// L'invariant — le cumul des acomptes d'un devis ne dépasse pas le montant
// engagé — ne peut pas être une contrainte de table : le montant engagé vit
// dans la table devis, qu'aucune contrainte d'ici n'a le droit de regarder
// (R2). Il est donc tenu par cette transaction :
//
//  1. un verrou consultatif transactionnel est pris sur un hachage du devisID
//     (pg_advisory_xact_lock). Deux insertions du même devis se sérialisent
//     dessus, celles de devis différents ne se gênent pas ; le verrou tombe
//     avec la transaction, commit ou rollback ;
//  2. le cumul existant est relu SOUS ce verrou — c'est la relecture qui fait
//     foi, celle du service ayant pu être doublée entre-temps ;
//  3. l'insertion ne part que si le cumul tient.
//
// hashtext peut faire coïncider deux devisID sur la même clé de verrou : deux
// saisies de devis différents se sérialiseraient alors sans nécessité — un
// ralentissement théorique, jamais un faux refus, puisque la relecture du
// cumul filtre sur le devisID exact.
//
// Un acompte sans devisID entre sans transaction ni verrou : rien d'engagé à
// comparer, l'invariant ne le concerne pas.
func (f *FinanceRepo) CreateAcompte(ctx context.Context, acompte finance.Acompte, montantEngage finance.Montant) error {
	id, auteur, err := financeWriteIDs(acompte.ID.String(), acompte.RecordedBy.String(), "acompte")
	if err != nil {
		return err
	}

	const insert = `
		INSERT INTO acomptes (` + acompteColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	args := []any{
		id, acompte.DevisID, acompte.Entreprise, int64(acompte.Montant), acompte.Date,
		acompte.Moyen.String(), acompte.Notes,
		acompte.Assurance.Statut.String(), optionalTime(acompte.Assurance.SentAt),
		int64(acompte.Assurance.MontantRembourse), optionalTime(acompte.Assurance.RefundedAt),
		auteur, acompte.CreatedAt, acompte.UpdatedAt,
	}

	if acompte.DevisID == "" {
		if _, execErr := f.pool.Exec(ctx, insert, args...); execErr != nil {
			return fmt.Errorf("insertion de l'acompte %s : %w", acompte.ID, execErr)
		}
		return nil
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ouverture de la transaction d'acompte : %w", err)
	}
	// Le rollback d'une transaction déjà validée est sans effet : c'est le
	// filet du chemin d'erreur, pas une annulation du chemin heureux.
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // annulation de secours, sans conséquence après un Commit réussi.

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, acompte.DevisID); err != nil {
		return fmt.Errorf("verrouillage des acomptes du devis %s : %w", acompte.DevisID, err)
	}

	var cumul int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(montant), 0) FROM acomptes WHERE devis_id = $1`,
		acompte.DevisID).Scan(&cumul); err != nil {
		return fmt.Errorf("relecture du cumul des acomptes du devis %s : %w", acompte.DevisID, err)
	}

	if finance.Montant(cumul)+acompte.Montant > montantEngage {
		return fmt.Errorf("%w : %d centimes déjà versés, %s demandés, %s engagés",
			finance.ErrAcomptesExceedEngagement, cumul, acompte.Montant, montantEngage)
	}

	if _, err := tx.Exec(ctx, insert, args...); err != nil {
		return fmt.Errorf("insertion de l'acompte %s : %w", acompte.ID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("validation de l'acompte %s : %w", acompte.ID, err)
	}

	return nil
}

// AcompteByID lit un acompte par son identifiant.
func (f *FinanceRepo) AcompteByID(ctx context.Context, acompteID finance.ID) (finance.Acompte, error) {
	id, err := lookupUUID(acompteID.String(), finance.ErrUnknownAcompte)
	if err != nil {
		return finance.Acompte{}, err
	}

	const query = `SELECT ` + acompteColumns + ` FROM acomptes WHERE id = $1`

	acompte, err := scanAcompte(f.pool.QueryRow(ctx, query, id))
	if err != nil {
		return finance.Acompte{}, fmt.Errorf("lecture de l'acompte %s : %w", acompteID, err)
	}

	return acompte, nil
}

// ListAcomptes renvoie tous les acomptes, du plus récent au plus ancien.
func (f *FinanceRepo) ListAcomptes(ctx context.Context) ([]finance.Acompte, error) {
	const query = `SELECT ` + acompteColumns + ` FROM acomptes ORDER BY date_piece DESC, cree_le DESC, id`

	acomptes, err := queryAll(ctx, f.pool, scanAcompte, query)
	if err != nil {
		return nil, fmt.Errorf("lecture des acomptes : %w", err)
	}

	return acomptes, nil
}

// UpdateAcompte réécrit un acompte entier, sous la même garde optimiste que
// [FinanceRepo.UpdateFacture].
func (f *FinanceRepo) UpdateAcompte(ctx context.Context, acompte finance.Acompte, expected time.Time) error {
	id, err := writeUUID(acompte.ID.String(), "acompte")
	if err != nil {
		return err
	}

	const query = `
		UPDATE acomptes
		   SET devis_id = $2, entreprise = $3, montant = $4, date_piece = $5,
		       moyen = $6, notes = $7, statut_assurance = $8, envoyee_le = $9,
		       montant_rembourse = $10, rembourse_le = $11, modifie_le = $12
		 WHERE id = $1 AND modifie_le = $13`

	tag, err := f.pool.Exec(ctx, query,
		id, acompte.DevisID, acompte.Entreprise, int64(acompte.Montant), acompte.Date,
		acompte.Moyen.String(), acompte.Notes,
		acompte.Assurance.Statut.String(), optionalTime(acompte.Assurance.SentAt),
		int64(acompte.Assurance.MontantRembourse), optionalTime(acompte.Assurance.RefundedAt),
		acompte.UpdatedAt, expected)
	if err != nil {
		return fmt.Errorf("réécriture de l'acompte %s : %w", acompte.ID, err)
	}
	if tag.RowsAffected() == 0 {
		if _, readErr := f.AcompteByID(ctx, acompte.ID); readErr != nil {
			return readErr
		}
		return fmt.Errorf("%w : acompte %s", finance.ErrConcurrentUpdate, acompte.ID)
	}

	return nil
}

// SumAcomptesByDevis rend le cumul des acomptes d'un devis. La lecture est
// hors verrou : elle sert au refus rapide du service et à la synthèse, pas à
// l'invariant — c'est la relecture de [FinanceRepo.CreateAcompte] qui fait foi.
func (f *FinanceRepo) SumAcomptesByDevis(ctx context.Context, devisID string) (finance.Montant, error) {
	var cumul int64
	if err := f.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(montant), 0) FROM acomptes WHERE devis_id = $1`,
		devisID).Scan(&cumul); err != nil {
		return 0, fmt.Errorf("cumul des acomptes du devis %s : %w", devisID, err)
	}

	return finance.Montant(cumul), nil
}

// financeWriteIDs traduit les deux identifiants qu'une écriture porte toujours :
// celui de la pièce et celui de l'acteur qui l'a saisie.
func financeWriteIDs(pieceID, acteurID, label string) (piece, auteur pgtype.UUID, err error) {
	piece, err = writeUUID(pieceID, label)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	auteur, err = writeUUID(acteurID, "acteur")
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}

	return piece, auteur, nil
}

// scanFacture reconstruit une facture depuis une ligne.
func scanFacture(row rowScanner) (finance.Facture, error) {
	var (
		facture     finance.Facture
		id          pgtype.UUID
		montant     int64
		paiement    string
		payeeLe     pgtype.Timestamptz
		assurance   string
		envoyeeLe   pgtype.Timestamptz
		rembourse   int64
		rembourseLe pgtype.Timestamptz
		auteur      pgtype.UUID
	)

	err := row.Scan(&id, &facture.DevisID, &facture.Entreprise, &montant, &facture.Date,
		&facture.Numero, &facture.Notes, &paiement, &payeeLe,
		&assurance, &envoyeeLe, &rembourse, &rembourseLe,
		&auteur, &facture.CreatedAt, &facture.UpdatedAt)
	if err != nil {
		return finance.Facture{}, financeScanError(err, finance.ErrUnknownFacture)
	}

	facture.ID = finance.ID(id.String())
	facture.Montant = finance.Montant(montant)
	facture.Paiement = finance.StatutPaiement(paiement)
	if payeeLe.Valid {
		facture.PaidAt = payeeLe.Time
	}
	facture.Assurance = scanSuiviAssurance(assurance, envoyeeLe, rembourse, rembourseLe)
	facture.RecordedBy = finance.ActeurID(auteur.String())

	return facture, nil
}

// scanAcompte reconstruit un acompte depuis une ligne.
func scanAcompte(row rowScanner) (finance.Acompte, error) {
	var (
		acompte     finance.Acompte
		id          pgtype.UUID
		montant     int64
		moyen       string
		assurance   string
		envoyeeLe   pgtype.Timestamptz
		rembourse   int64
		rembourseLe pgtype.Timestamptz
		auteur      pgtype.UUID
	)

	err := row.Scan(&id, &acompte.DevisID, &acompte.Entreprise, &montant, &acompte.Date,
		&moyen, &acompte.Notes, &assurance, &envoyeeLe, &rembourse, &rembourseLe,
		&auteur, &acompte.CreatedAt, &acompte.UpdatedAt)
	if err != nil {
		return finance.Acompte{}, financeScanError(err, finance.ErrUnknownAcompte)
	}

	acompte.ID = finance.ID(id.String())
	acompte.Montant = finance.Montant(montant)
	acompte.Moyen = finance.MoyenPaiement(moyen)
	acompte.Assurance = scanSuiviAssurance(assurance, envoyeeLe, rembourse, rembourseLe)
	acompte.RecordedBy = finance.ActeurID(auteur.String())

	return acompte, nil
}

// financeScanError traduit l'absence de ligne dans le vocabulaire du domaine.
func financeScanError(err, unknown error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return unknown
	}
	return err
}

// scanSuiviAssurance reconstruit le suivi assurance depuis ses quatre colonnes.
func scanSuiviAssurance(statut string, envoyeeLe pgtype.Timestamptz, rembourse int64, rembourseLe pgtype.Timestamptz) finance.SuiviAssurance {
	suivi := finance.SuiviAssurance{
		Statut:           finance.StatutAssurance(statut),
		MontantRembourse: finance.Montant(rembourse),
	}
	if envoyeeLe.Valid {
		suivi.SentAt = envoyeeLe.Time
	}
	if rembourseLe.Valid {
		suivi.RefundedAt = rembourseLe.Time
	}

	return suivi
}
