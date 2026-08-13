package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// retenuUniqueConstraint est le nom de l'index unique partiel qui garantit
// qu'une demande ne porte qu'un seul devis retenu.
//
// Le tester nommément évite de traduire en « demande déjà tranchée » la
// violation d'une *autre* contrainte d'unicité que la table gagnerait plus tard.
const retenuUniqueConstraint = "devis_un_seul_retenu_par_demande"

// demandeOuverteConstraint est le nom que le trigger « une demande close
// n'accepte plus de devis reçu » donne à son refus (migration 00007).
//
// Le trigger le pose lui-même dans le champ « contrainte » de l'erreur, ce qui
// permet de le reconnaître comme n'importe quelle contrainte de table — sans
// dépendre du texte du message, qui, lui, se reformule.
const demandeOuverteConstraint = "devis_demande_ouverte"

// raiseExceptionCode est le SQLSTATE des exceptions levées par RAISE dans une
// fonction PL/pgSQL. Il ne suffit pas à identifier la nôtre — tout RAISE sans
// ERRCODE explicite le porte — d'où la vérification du nom de contrainte.
const raiseExceptionCode = "P0001"

// DevisRepo implémente [devis.Repository] sur PostgreSQL.
//
// Les colonnes portent le vocabulaire du domaine (lot, entreprise, montant,
// statut, recu_le) : c'est le même modèle vu des deux côtés, et la
// correspondance se lit sans table de traduction.
type DevisRepo struct {
	pool *pgxpool.Pool
}

// NewDevisRepo construit le dépôt sur un pool de connexions existant. Le pool
// reste la propriété de l'appelant — cmd/avanti, qui l'ouvre et le ferme.
func NewDevisRepo(pool *pgxpool.Pool) (*DevisRepo, error) {
	if pool == nil {
		return nil, errors.New("postgres : pool de connexions manquant")
	}
	return &DevisRepo{pool: pool}, nil
}

// demandeColumns est la liste de sélection commune aux lectures de demandes. La
// factoriser garantit que les colonnes arrivent dans l'ordre qu'attend
// [scanDemande].
const demandeColumns = `id, lot, description, artisans, envoyee_le, cree_par, cree_le, modifie_le`

// devisColumns est la liste de sélection commune aux lectures de devis.
const devisColumns = `id, demande_id, entreprise, email, telephone, montant, recu_le, validite, ` +
	`notes, statut, document_ids, saisi_par, decide_par, decide_le, cree_le, modifie_le`

// artisanRow est la forme JSON d'un artisan sollicité.
//
// Les noms de champs sont fixés ici plutôt qu'hérités de la structure du
// domaine : renommer un champ Go ne doit pas rendre illisibles les lignes déjà
// écrites.
type artisanRow struct {
	Entreprise string `json:"entreprise"`
	Email      string `json:"email,omitempty"`
	Telephone  string `json:"telephone,omitempty"`
}

// CreateDemande insère une demande de devis.
func (d *DevisRepo) CreateDemande(ctx context.Context, demande devis.DemandeDevis) error {
	id, err := devisUUID(demande.ID.String(), "demande de devis")
	if err != nil {
		return err
	}
	auteur, err := devisUUID(demande.CreatedBy.String(), "acteur")
	if err != nil {
		return err
	}
	artisans, err := marshalArtisans(demande.Artisans)
	if err != nil {
		return err
	}

	const query = `
		INSERT INTO demandes_devis (` + demandeColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	if _, err := d.pool.Exec(ctx, query,
		id, demande.Lot, demande.Description, artisans,
		demande.SentAt, auteur, demande.CreatedAt, demande.UpdatedAt); err != nil {
		return fmt.Errorf("insertion de la demande de devis %s : %w", demande.ID, err)
	}

	return nil
}

// DemandeByID lit une demande par son identifiant.
func (d *DevisRepo) DemandeByID(ctx context.Context, demandeID devis.ID) (devis.DemandeDevis, error) {
	id, err := lookupUUID(demandeID.String(), devis.ErrUnknownDemande)
	if err != nil {
		return devis.DemandeDevis{}, err
	}

	const query = `SELECT ` + demandeColumns + ` FROM demandes_devis WHERE id = $1`

	demande, err := scanDemande(d.pool.QueryRow(ctx, query, id))
	if err != nil {
		return devis.DemandeDevis{}, fmt.Errorf("lecture de la demande de devis %s : %w", demandeID, err)
	}

	return demande, nil
}

// ListDemandes renvoie toutes les demandes, de la plus récemment envoyée à la
// plus ancienne.
//
// Sans pagination : une reconstruction de maison compte quelques dizaines de
// lots de travaux. Le jour où ce ne serait plus vrai, c'est le port du domaine
// qu'il faudrait revoir, pas seulement cette requête.
func (d *DevisRepo) ListDemandes(ctx context.Context) ([]devis.DemandeDevis, error) {
	const query = `SELECT ` + demandeColumns + ` FROM demandes_devis ORDER BY envoyee_le DESC, cree_le DESC`

	demandes, err := queryAll(ctx, d.pool, scanDemande, query)
	if err != nil {
		return nil, fmt.Errorf("lecture des demandes de devis : %w", err)
	}

	return demandes, nil
}

// CreateDevis insère un devis reçu.
//
// Le refus d'une demande déjà tranchée n'est pas décidé ici : c'est le trigger
// de la migration 00007 qui le prononce, et l'insertion se contente de le
// traduire dans le vocabulaire du domaine. Un contrôle en Go, aussi bien placé
// soit-il, lirait un état que la rétention concurrente peut démentir entre la
// lecture et l'écriture.
func (d *DevisRepo) CreateDevis(ctx context.Context, proposition devis.Devis) error {
	id, err := devisUUID(proposition.ID.String(), "devis")
	if err != nil {
		return err
	}
	demandeID, err := devisUUID(proposition.DemandeID.String(), "demande de devis")
	if err != nil {
		return err
	}
	auteur, err := devisUUID(proposition.RecordedBy.String(), "acteur")
	if err != nil {
		return err
	}

	const query = `
		INSERT INTO devis (` + devisColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

	_, err = d.pool.Exec(ctx, query,
		id, demandeID, proposition.Artisan.Entreprise, proposition.Artisan.Email, proposition.Artisan.Telephone,
		int64(proposition.Montant), proposition.ReceivedAt, toInterval(proposition.Validity),
		proposition.Notes, proposition.Statut.String(), documentIDs(proposition.DocumentIDs), auteur,
		optionalUUID(proposition.DecidedBy), optionalTime(proposition.DecidedAt),
		proposition.CreatedAt, proposition.UpdatedAt)
	switch {
	case isDemandeCloseConflict(err):
		return fmt.Errorf("%w : %s", devis.ErrDemandeClosed, proposition.DemandeID)
	case err != nil:
		return fmt.Errorf("insertion du devis %s : %w", proposition.ID, err)
	}

	return nil
}

// DevisByID lit un devis par son identifiant.
func (d *DevisRepo) DevisByID(ctx context.Context, devisID devis.ID) (devis.Devis, error) {
	id, err := lookupUUID(devisID.String(), devis.ErrUnknownDevis)
	if err != nil {
		return devis.Devis{}, err
	}

	const query = `SELECT ` + devisColumns + ` FROM devis WHERE id = $1`

	proposition, err := scanDevis(d.pool.QueryRow(ctx, query, id))
	if err != nil {
		return devis.Devis{}, fmt.Errorf("lecture du devis %s : %w", devisID, err)
	}

	return proposition, nil
}

// ListDevisByDemande renvoie les devis d'une demande, du moins-disant au
// plus-disant.
func (d *DevisRepo) ListDevisByDemande(ctx context.Context, demandeID devis.ID) ([]devis.Devis, error) {
	id, err := lookupUUID(demandeID.String(), devis.ErrUnknownDemande)
	if err != nil {
		return nil, err
	}

	const query = `SELECT ` + devisColumns + ` FROM devis WHERE demande_id = $1 ORDER BY montant, recu_le, id`

	return d.queryDevis(ctx, query, id)
}

// ListDevis renvoie tous les devis, toutes demandes confondues.
func (d *DevisRepo) ListDevis(ctx context.Context) ([]devis.Devis, error) {
	const query = `SELECT ` + devisColumns + ` FROM devis ORDER BY demande_id, montant, recu_le, id`

	return d.queryDevis(ctx, query)
}

// queryDevis exécute une lecture multiple et décode les lignes.
func (d *DevisRepo) queryDevis(ctx context.Context, query string, args ...any) ([]devis.Devis, error) {
	propositions, err := queryAll(ctx, d.pool, scanDevis, query, args...)
	if err != nil {
		return nil, fmt.Errorf("lecture des devis : %w", err)
	}

	return propositions, nil
}

// rowScanner est l'intersection des interfaces de pgx.Row et pgx.Rows. C'est ce
// qui permet à un décodeur de servir aussi bien une lecture unitaire qu'un
// parcours — donc de n'avoir qu'un seul endroit où l'ordre des colonnes compte.
type rowScanner interface {
	Scan(dest ...any) error
}

// queryAll exécute une lecture multiple et décode chaque ligne.
//
// La fonction est générique parce que la boucle, elle, ne dépend pas du type
// décodé : ouvrir, parcourir, refermer, et surtout ne pas oublier rows.Err() —
// l'erreur qui n'arrive qu'à la fin du parcours et qu'une boucle recopiée
// oublie une fois sur deux.
func queryAll[T any](ctx context.Context, pool *pgxpool.Pool, scan func(rowScanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var collected []T
	for rows.Next() {
		decoded, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		collected = append(collected, decoded)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return collected, nil
}

// Retain retient un devis et refuse ses concurrents en une seule transaction.
//
// L'indivisibilité n'est pas une précaution de style : retenir et refuser sont
// une seule décision. Une base qui laisserait passer la moitié de l'opération
// produirait une demande sans devis retenu mais avec des concurrents refusés,
// c'est-à-dire une comparaison qu'on ne peut plus ni lire ni reprendre.
//
// La transaction commence par verrouiller la ligne de la demande. C'est le
// rendez-vous que se donnent la décision et l'arrivée d'un devis, du côté où le
// trigger de la migration 00007 l'attend : ou bien un devis reçu se glisse avant
// la décision, et le refus des concurrents l'emporte avec les autres, ou bien il
// arrive après, et le trigger le refuse. Sans ce verrou, il resterait une fenêtre
// où un devis atterrit sur une demande que la décision vient de clore.
//
// L'ordre des deux écritures compte ensuite. Le devis choisi passe retenu
// *d'abord*, ce qui fait mordre l'index unique partiel immédiatement si un
// concurrent l'est déjà : la transaction est refusée avant d'avoir refusé qui
// que ce soit.
func (d *DevisRepo) Retain(ctx context.Context, devisID devis.ID, by devis.ActeurID, at time.Time) error {
	id, err := lookupUUID(devisID.String(), devis.ErrUnknownDevis)
	if err != nil {
		return err
	}
	decideur, err := devisUUID(by.String(), "acteur")
	if err != nil {
		return err
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ouverture de la transaction de décision : %w", err)
	}
	// Le rollback d'une transaction déjà validée est sans effet : c'est le
	// filet du chemin d'erreur, pas une annulation du chemin heureux.
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // annulation de secours, sans conséquence après un Commit réussi.

	// Seule la ligne de la demande est verrouillée — FOR UPDATE OF dd — et non
	// le devis lu au passage : c'est la demande qui sert de rendez-vous, et
	// verrouiller un devis n'empêcherait pas d'en insérer un autre.
	const verrouQuery = `
		SELECT dd.id
		  FROM devis AS d
		  JOIN demandes_devis AS dd ON dd.id = d.demande_id
		 WHERE d.id = $1
		   FOR UPDATE OF dd`

	var demandeID pgtype.UUID
	switch verrouErr := tx.QueryRow(ctx, verrouQuery, id).Scan(&demandeID); {
	case errors.Is(verrouErr, pgx.ErrNoRows):
		// La clé étrangère garantit qu'un devis a sa demande : aucune ligne ici
		// veut dire que le devis lui-même n'existe pas.
		return d.explainMissedUpdate(ctx, devisID)
	case verrouErr != nil:
		return fmt.Errorf("verrouillage de la demande du devis %s : %w", devisID, verrouErr)
	}

	const retenirQuery = `
		UPDATE devis
		   SET statut     = 'retenu',
		       decide_par = $2,
		       decide_le  = $3,
		       modifie_le = $3
		 WHERE id = $1 AND statut = 'recu'`

	tag, err := tx.Exec(ctx, retenirQuery, id, decideur, at)
	switch {
	case isRetenuConflict(err):
		return fmt.Errorf("%w : %s", devis.ErrDemandeClosed, devisID)
	case err != nil:
		return fmt.Errorf("retenue du devis %s : %w", devisID, err)
	case tag.RowsAffected() == 0:
		return d.explainMissedUpdate(ctx, devisID)
	}

	// Le ricochet : les concurrents encore en attente sont écartés par la même
	// décision. Un devis déjà refusé n'est pas retouché — sa date de décision
	// est celle de son propre refus.
	const refuserQuery = `
		UPDATE devis
		   SET statut     = 'refuse',
		       decide_par = $3,
		       decide_le  = $4,
		       modifie_le = $4
		 WHERE demande_id = $1 AND id <> $2 AND statut = 'recu'`

	if _, err := tx.Exec(ctx, refuserQuery, demandeID, id, decideur, at); err != nil {
		return fmt.Errorf("refus des devis concurrents de %s : %w", devisID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("validation de la décision sur le devis %s : %w", devisID, err)
	}

	return nil
}

// Reject refuse un devis sans rien retenir. Les autres devis de la demande ne
// bougent pas : écarter une offre n'est pas en choisir une, et une seule ligne
// change — d'où l'absence de transaction ici.
func (d *DevisRepo) Reject(ctx context.Context, devisID devis.ID, by devis.ActeurID, at time.Time) error {
	id, err := lookupUUID(devisID.String(), devis.ErrUnknownDevis)
	if err != nil {
		return err
	}
	decideur, err := devisUUID(by.String(), "acteur")
	if err != nil {
		return err
	}

	const query = `
		UPDATE devis
		   SET statut     = 'refuse',
		       decide_par = $2,
		       decide_le  = $3,
		       modifie_le = $3
		 WHERE id = $1 AND statut = 'recu'`

	tag, err := d.pool.Exec(ctx, query, id, decideur, at)
	if err != nil {
		return fmt.Errorf("refus du devis %s : %w", devisID, err)
	}
	if tag.RowsAffected() == 0 {
		return d.explainMissedUpdate(ctx, devisID)
	}

	return nil
}

// explainMissedUpdate dit pourquoi une décision n'a touché aucune ligne.
//
// Les deux causes se traitent différemment côté appelant — un devis inconnu est
// une URL erronée, un devis déjà tranché est une course entre deux personnes —
// et l'UPDATE ne les distingue pas. Une lecture de plus, sur le seul chemin
// d'échec, achète cette distinction.
func (d *DevisRepo) explainMissedUpdate(ctx context.Context, devisID devis.ID) error {
	if _, err := d.DevisByID(ctx, devisID); err != nil {
		return err
	}

	return fmt.Errorf("%w : %s", devis.ErrDevisAlreadyDecided, devisID)
}

// scanDemande reconstruit une demande depuis une ligne.
func scanDemande(row rowScanner) (devis.DemandeDevis, error) {
	var (
		demande  devis.DemandeDevis
		id       pgtype.UUID
		auteur   pgtype.UUID
		artisans []byte
	)

	err := row.Scan(&id, &demande.Lot, &demande.Description, &artisans,
		&demande.SentAt, &auteur, &demande.CreatedAt, &demande.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return devis.DemandeDevis{}, devis.ErrUnknownDemande
	}
	if err != nil {
		return devis.DemandeDevis{}, err
	}

	demande.Artisans, err = unmarshalArtisans(artisans)
	if err != nil {
		return devis.DemandeDevis{}, err
	}

	demande.ID = devis.ID(id.String())
	demande.CreatedBy = devis.ActeurID(auteur.String())

	return demande, nil
}

// scanDevis reconstruit un devis depuis une ligne.
func scanDevis(row rowScanner) (devis.Devis, error) {
	var (
		proposition devis.Devis
		id          pgtype.UUID
		demandeID   pgtype.UUID
		montant     int64
		validite    pgtype.Interval
		statut      string
		documents   []string
		saisiPar    pgtype.UUID
		decidePar   pgtype.UUID
		decideLe    pgtype.Timestamptz
	)

	err := row.Scan(&id, &demandeID,
		&proposition.Artisan.Entreprise, &proposition.Artisan.Email, &proposition.Artisan.Telephone,
		&montant, &proposition.ReceivedAt, &validite, &proposition.Notes, &statut,
		&documents, &saisiPar, &decidePar, &decideLe,
		&proposition.CreatedAt, &proposition.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return devis.Devis{}, devis.ErrUnknownDevis
	}
	if err != nil {
		return devis.Devis{}, err
	}

	proposition.ID = devis.ID(id.String())
	proposition.DemandeID = devis.ID(demandeID.String())
	proposition.Montant = devis.Montant(montant)
	if len(documents) > 0 {
		proposition.DocumentIDs = documents
	}
	proposition.Validity = fromInterval(validite)
	proposition.Statut = devis.Statut(statut)
	proposition.RecordedBy = devis.ActeurID(saisiPar.String())
	if decidePar.Valid {
		proposition.DecidedBy = devis.ActeurID(decidePar.String())
	}
	if decideLe.Valid {
		proposition.DecidedAt = decideLe.Time
	}

	return proposition, nil
}

// marshalArtisans encode les artisans sollicités pour la colonne JSONB. Une
// liste vide s'écrit « [] » et non « null » : la contrainte de table exige un
// tableau.
func marshalArtisans(artisans []devis.Artisan) ([]byte, error) {
	rows := make([]artisanRow, 0, len(artisans))
	for _, artisan := range artisans {
		rows = append(rows, artisanRow{
			Entreprise: artisan.Entreprise,
			Email:      artisan.Email,
			Telephone:  artisan.Telephone,
		})
	}

	encoded, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("encodage des artisans sollicités : %w", err)
	}

	return encoded, nil
}

// unmarshalArtisans décode la colonne JSONB.
func unmarshalArtisans(raw []byte) ([]devis.Artisan, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var rows []artisanRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("décodage des artisans sollicités : %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	artisans := make([]devis.Artisan, 0, len(rows))
	for _, row := range rows {
		artisans = append(artisans, devis.Artisan{
			Entreprise: row.Entreprise,
			Email:      row.Email,
			Telephone:  row.Telephone,
		})
	}

	return artisans, nil
}

// documentIDs rend la liste des pièces jointes sous une forme que la colonne
// accepte.
//
// Une tranche Go nulle s'encode en NULL, que la table refuse : « aucune pièce »
// s'écrit comme un tableau vide, pas comme une absence de valeur. La lecture
// fait le chemin inverse, de sorte qu'un aller-retour rende exactement ce qui a
// été écrit.
func documentIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// toInterval traduit une durée de validité en INTERVAL PostgreSQL.
//
// Tout est écrit en microsecondes, jamais en jours ni en mois : ces deux unités
// sont ambiguës pour PostgreSQL — un mois n'a pas de durée fixe — et une durée
// Go, elle, en a une. Le retour par [fromInterval] est donc exact.
func toInterval(validity time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: validity.Microseconds(), Valid: true}
}

// fromInterval traduit un INTERVAL en durée.
//
// Les jours et les mois sont traduits à titre de repli, avec les équivalences
// usuelles : ce dépôt n'en écrit jamais, mais une correction passée en psql
// pourrait en poser, et rendre zéro ferait alors disparaître silencieusement
// une validité annoncée.
func fromInterval(interval pgtype.Interval) time.Duration {
	if !interval.Valid {
		return 0
	}

	return time.Duration(interval.Microseconds)*time.Microsecond +
		time.Duration(interval.Days)*24*time.Hour +
		time.Duration(interval.Months)*30*24*time.Hour
}

// devisUUID traduit un identifiant du domaine dans le type uuid de PostgreSQL.
//
// La conversion est explicite plutôt que laissée à pgx : le pilote encode le
// type uuid en binaire, où une chaîne Go n'a pas de plan d'encodage. La traduire
// ici a un second mérite — un identifiant mal formé est refusé avant d'atteindre
// le SQL, avec un message qui nomme la valeur fautive.
func devisUUID(raw, label string) (pgtype.UUID, error) {
	var uuid pgtype.UUID
	if err := uuid.Scan(raw); err != nil {
		return pgtype.UUID{}, fmt.Errorf("identifiant de %s %q illisible comme uuid : %w", label, raw, err)
	}
	return uuid, nil
}

// lookupUUID traduit l'identifiant d'une *recherche* et rend l'erreur « inconnu »
// du domaine quand il est illisible.
//
// La distinction avec [devisUUID] est délibérée. Sur une lecture ou une
// décision, l'identifiant vient d'une URL : un identifiant malformé ne désigne
// rien, exactement comme un identifiant bien formé qui n'existe pas, et
// l'appelant doit pouvoir traiter les deux de la même façon — une page
// introuvable, pas une panne. Sur une écriture, en revanche, l'identifiant vient
// du domaine : le moindre défaut y est un bug, et il ressort en erreur brute.
func lookupUUID(raw string, unknown error) (pgtype.UUID, error) {
	var uuid pgtype.UUID
	if err := uuid.Scan(raw); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w : identifiant %q illisible comme uuid", unknown, raw)
	}
	return uuid, nil
}

// optionalUUID rend l'identifiant du décideur, ou une valeur NULL tant que le
// devis n'est pas tranché.
func optionalUUID(acteur devis.ActeurID) pgtype.UUID {
	if acteur == "" {
		return pgtype.UUID{}
	}

	uuid, err := devisUUID(acteur.String(), "acteur")
	if err != nil {
		return pgtype.UUID{}
	}

	return uuid
}

// optionalTime rend la date de décision, ou une valeur NULL tant qu'il n'y en a
// pas.
func optionalTime(instant time.Time) pgtype.Timestamptz {
	if instant.IsZero() {
		return pgtype.Timestamptz{}
	}

	return pgtype.Timestamptz{Time: instant, Valid: true}
}

// isRetenuConflict reconnaît la violation de l'index unique partiel « un seul
// retenu par demande ».
func isRetenuConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	return pgErr.Code == uniqueViolationCode && pgErr.ConstraintName == retenuUniqueConstraint
}

// isDemandeCloseConflict reconnaît le refus du trigger « une demande close
// n'accepte plus de devis reçu ».
func isDemandeCloseConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	return pgErr.Code == raiseExceptionCode && pgErr.ConstraintName == demandeOuverteConstraint
}
