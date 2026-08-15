package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// planningLockID est la clé du verrou consultatif transactionnel qui sérialise
// TOUTES les écritures d'étapes — création, modification, transitions.
//
// Un seul verrou pour tout le planning, et c'est un choix délibéré : une
// instance d'Avanti porte UN chantier, soit quelques dizaines d'étapes et une
// poignée de personnes. Une granularité plus fine — par composante connexe du
// graphe, par étape et ses voisines — n'achèterait aucune concurrence utile,
// mais offrirait de vraies occasions de se tromper : deux verrous « fins » qui
// oublient une arête et laissent deux éditions fermer un cycle à elles deux.
// Le verrou tombe avec la transaction, commit ou rollback.
//
// La valeur est arbitraire et n'a qu'une exigence : ne pas entrer en collision
// avec les autres verrous consultatifs du dépôt (les acomptes utilisent
// hashtext(devis_id), voir finance.go). Une collision ne serait de toute façon
// qu'une sérialisation de trop, jamais une erreur.
const planningLockID int64 = 0x6176616e74695f70 // « avanti_p » en ASCII.

// PlanningRepo implémente [planning.Repository] sur PostgreSQL.
//
// Les colonnes portent le vocabulaire du domaine (nom, debut_prevu, fin_reelle,
// prerequis) : c'est le même modèle vu des deux côtés, et la correspondance se
// lit sans table de traduction.
type PlanningRepo struct {
	pool *pgxpool.Pool
}

// NewPlanningRepo construit le dépôt sur un pool de connexions existant. Le
// pool reste la propriété de l'appelant — cmd/avanti, qui l'ouvre et le ferme.
func NewPlanningRepo(pool *pgxpool.Pool) (*PlanningRepo, error) {
	if pool == nil {
		return nil, errors.New("postgres : pool de connexions manquant")
	}
	return &PlanningRepo{pool: pool}, nil
}

// etapeColumns est la liste de sélection commune aux lectures d'étapes. La
// factoriser garantit que les colonnes arrivent dans l'ordre qu'attend
// [scanEtape]. Les dépendances ne sont pas là : elles vivent dans leur table
// et se lisent à part.
const etapeColumns = `id, nom, description, debut_prevu, fin_prevue, debut_reel, fin_reelle, ` +
	`devis_id, cree_par, cree_le, modifie_le`

// jalonColumns est la liste de sélection commune aux lectures de jalons.
const jalonColumns = `id, nom, date_prevue, atteint_le, cree_par, cree_le, modifie_le`

// pgQuerier est ce qu'une lecture demande à sa connexion : le pool et une
// transaction le satisfont tous deux. C'est ce qui permet de relire le graphe
// SOUS le verrou — donc dans la transaction — avec le même code que les
// lectures ordinaires.
type pgQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CreateEtape insère une étape et ses dépendances, sous le verrou du planning.
//
// Le contrat du port exige de rejouer ici ce que le service a vérifié hors
// verrou : l'existence des prérequis et l'acyclicité du graphe. La transaction
// fait donc, dans l'ordre : verrou consultatif, relecture du graphe entier,
// vérifications sur cet état — le seul qui fasse foi —, puis écriture. Deux
// créations ou éditions simultanées se sérialisent sur le verrou, et la
// seconde voit ce que la première a écrit.
func (p *PlanningRepo) CreateEtape(ctx context.Context, etape planning.Etape) error {
	id, auteur, err := writeIDs(etape.ID.String(), etape.CreatedBy.String(), "étape")
	if err != nil {
		return err
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ouverture de la transaction d'étape : %w", err)
	}
	// Le rollback d'une transaction déjà validée est sans effet : c'est le
	// filet du chemin d'erreur, pas une annulation du chemin heureux.
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // annulation de secours, sans conséquence après un Commit réussi.

	if err := acquirePlanningLock(ctx, tx); err != nil {
		return err
	}

	if err := checkEtapeGraph(ctx, tx, etape); err != nil {
		return err
	}

	const insert = `
		INSERT INTO etapes (` + etapeColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	if _, err := tx.Exec(ctx, insert,
		id, etape.Name, etape.Description, etape.PlannedStart, etape.PlannedEnd,
		optionalTime(etape.ActualStart), optionalTime(etape.ActualEnd),
		etape.DevisID, auteur, etape.CreatedAt, etape.UpdatedAt); err != nil {
		return fmt.Errorf("insertion de l'étape %s : %w", etape.ID, err)
	}

	if err := insertDependencies(ctx, tx, etape); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("validation de l'étape %s : %w", etape.ID, err)
	}

	return nil
}

// EtapeByID lit une étape et ses dépendances.
func (p *PlanningRepo) EtapeByID(ctx context.Context, etapeID planning.ID) (planning.Etape, error) {
	id, err := lookupUUID(etapeID.String(), planning.ErrUnknownEtape)
	if err != nil {
		return planning.Etape{}, err
	}

	const query = `SELECT ` + etapeColumns + ` FROM etapes WHERE id = $1`

	etape, err := scanEtape(p.pool.QueryRow(ctx, query, id))
	if err != nil {
		return planning.Etape{}, fmt.Errorf("lecture de l'étape %s : %w", etapeID, err)
	}

	deps, err := queryAll(ctx, p.pool, scanDependency,
		`SELECT etape_id, prerequis_id FROM etape_dependances WHERE etape_id = $1 ORDER BY prerequis_id`, id)
	if err != nil {
		return planning.Etape{}, fmt.Errorf("lecture des prérequis de l'étape %s : %w", etapeID, err)
	}
	for _, dep := range deps {
		etape.DependsOn = append(etape.DependsOn, dep.prerequis)
	}

	return etape, nil
}

// ListEtapes renvoie toutes les étapes avec leurs dépendances, triées par
// début prévu puis identifiant.
//
// Deux requêtes quel que soit le nombre d'étapes : les étapes d'un bloc, puis
// toutes les dépendances d'un bloc, assemblées en mémoire — un aller-retour
// par étape ferait grandir la lecture au rythme du chantier (le modèle des
// Totaux du domaine finance). Sans pagination : une reconstruction compte
// quelques dizaines de lots, et le jour où ce ne serait plus vrai, c'est le
// port du domaine qu'il faudrait revoir.
func (p *PlanningRepo) ListEtapes(ctx context.Context) ([]planning.Etape, error) {
	return listEtapes(ctx, p.pool)
}

// listEtapes est la lecture partagée entre le pool et les transactions : c'est
// elle que les écritures rappellent SOUS le verrou pour rejouer les
// vérifications de graphe.
func listEtapes(ctx context.Context, q pgQuerier) ([]planning.Etape, error) {
	etapes, err := queryAll(ctx, q, scanEtape,
		`SELECT `+etapeColumns+` FROM etapes ORDER BY debut_prevu, id`)
	if err != nil {
		return nil, fmt.Errorf("lecture des étapes : %w", err)
	}

	deps, err := queryAll(ctx, q, scanDependency,
		`SELECT etape_id, prerequis_id FROM etape_dependances ORDER BY etape_id, prerequis_id`)
	if err != nil {
		return nil, fmt.Errorf("lecture des dépendances d'étapes : %w", err)
	}

	byEtape := make(map[planning.ID][]planning.ID, len(deps))
	for _, dep := range deps {
		byEtape[dep.etape] = append(byEtape[dep.etape], dep.prerequis)
	}
	for i := range etapes {
		etapes[i].DependsOn = byEtape[etapes[i].ID]
	}

	return etapes, nil
}

// UpdateEtape réécrit une étape entière — dépendances comprises — sous garde
// optimiste et sous le verrou du planning.
//
// La garde est la clause `modifie_le = expected` : une écriture concurrente
// qui a changé la ligne entre la lecture et cette réécriture rend la clause
// fausse, et l'UPDATE ne touche rien — le perdant n'écrase pas ce que le
// gagnant a posé. Aucune ligne touchée se départage en relisant : étape
// disparue → inconnue (un 404), étape encore là →
// [planning.ErrConcurrentUpdate] (une course, la page se relit).
//
// Les dépendances sont réécrites par delete+insert — la ligne entière fait
// foi, comme pour le reste — puis les vérifications de graphe sont REJOUÉES
// sur l'état de la transaction : c'est ce rejeu, sérialisé par le verrou, qui
// interdit à deux éditions simultanées de fabriquer un cycle à elles deux.
func (p *PlanningRepo) UpdateEtape(ctx context.Context, etape planning.Etape, expected time.Time) error {
	id, err := writeUUID(etape.ID.String(), "étape")
	if err != nil {
		return err
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ouverture de la transaction d'étape : %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // annulation de secours, sans conséquence après un Commit réussi.

	if lockErr := acquirePlanningLock(ctx, tx); lockErr != nil {
		return lockErr
	}

	if writeErr := p.writeEtapeLocked(ctx, tx, etape, id, expected); writeErr != nil {
		return writeErr
	}

	const wipe = `DELETE FROM etape_dependances WHERE etape_id = $1`
	if _, err := tx.Exec(ctx, wipe, id); err != nil {
		return fmt.Errorf("réécriture des prérequis de l'étape %s : %w", etape.ID, err)
	}
	if err := insertDependencies(ctx, tx, etape); err != nil {
		return err
	}

	// Le rejeu qui fait foi : l'état de la transaction, réécriture comprise.
	if err := checkEtapeGraph(ctx, tx, etape); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("validation de l'étape %s : %w", etape.ID, err)
	}

	return nil
}

// StartEtape réécrit une étape démarrée, après avoir REJOUÉ sous le verrou la
// vérification que tous ses prérequis sont terminés.
//
// Le service a déjà vérifié — mais sa lecture a pu être doublée : entre elle
// et cette écriture, quelqu'un a pu retirer la fin réelle d'un prérequis ?
// Non — les transitions ne reculent pas — mais une édition concurrente a pu
// remplacer les prérequis eux-mêmes, et deux démarrages en chaîne peuvent se
// croiser. Le rejeu sous le même verrou que toutes les écritures d'étapes
// ferme la question : ce que cette transaction lit est ce qui est commité, et
// rien ne peut s'y glisser avant son propre commit.
func (p *PlanningRepo) StartEtape(ctx context.Context, etape planning.Etape, expected time.Time) error {
	id, err := writeUUID(etape.ID.String(), "étape")
	if err != nil {
		return err
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ouverture de la transaction de démarrage : %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // annulation de secours, sans conséquence après un Commit réussi.

	if lockErr := acquirePlanningLock(ctx, tx); lockErr != nil {
		return lockErr
	}

	// Les prérequis non terminés, relus sur l'état verrouillé, nommés pour le
	// message — la table est celle des dépendances COMMITÉES, pas celle que
	// l'appelant croit connaître.
	const pending = `
		SELECT e.nom
		  FROM etape_dependances AS d
		  JOIN etapes AS e ON e.id = d.prerequis_id
		 WHERE d.etape_id = $1 AND e.fin_reelle IS NULL
		 ORDER BY e.nom`

	blocking, err := queryAll(ctx, tx, scanName, pending, id)
	if err != nil {
		return fmt.Errorf("relecture des prérequis de l'étape %s : %w", etape.ID, err)
	}
	if len(blocking) > 0 {
		return fmt.Errorf("%w : %s", planning.ErrPrerequisitesNotDone, strings.Join(blocking, ", "))
	}

	if err := p.writeEtapeLocked(ctx, tx, etape, id, expected); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("validation du démarrage de l'étape %s : %w", etape.ID, err)
	}

	return nil
}

// writeEtapeLocked réécrit la ligne d'une étape sous garde optimiste, dans la
// transaction verrouillée de l'appelant.
func (p *PlanningRepo) writeEtapeLocked(ctx context.Context, tx pgx.Tx, etape planning.Etape, id pgtype.UUID, expected time.Time) error {
	const update = `
		UPDATE etapes
		   SET nom = $2, description = $3, debut_prevu = $4, fin_prevue = $5,
		       debut_reel = $6, fin_reelle = $7, devis_id = $8, modifie_le = $9
		 WHERE id = $1 AND modifie_le = $10`

	tag, err := tx.Exec(ctx, update,
		id, etape.Name, etape.Description, etape.PlannedStart, etape.PlannedEnd,
		optionalTime(etape.ActualStart), optionalTime(etape.ActualEnd),
		etape.DevisID, etape.UpdatedAt, expected)
	if err != nil {
		return fmt.Errorf("réécriture de l'étape %s : %w", etape.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return p.explainMissedEtapeUpdate(ctx, tx, etape.ID)
	}

	return nil
}

// explainMissedEtapeUpdate dit pourquoi une réécriture n'a touché aucune
// ligne — même partage que pour les pièces de finance : une étape inconnue est
// une URL erronée, une étape modifiée entre-temps est une course entre deux
// personnes, et l'UPDATE ne les distingue pas. Une lecture de plus, sur le
// seul chemin d'échec et dans la même transaction, achète cette distinction.
func (p *PlanningRepo) explainMissedEtapeUpdate(ctx context.Context, q pgQuerier, etapeID planning.ID) error {
	id, err := lookupUUID(etapeID.String(), planning.ErrUnknownEtape)
	if err != nil {
		return err
	}

	if _, err := scanEtape(q.QueryRow(ctx, `SELECT `+etapeColumns+` FROM etapes WHERE id = $1`, id)); err != nil {
		return fmt.Errorf("lecture de l'étape %s : %w", etapeID, err)
	}

	return fmt.Errorf("%w : étape %s", planning.ErrConcurrentUpdate, etapeID)
}

// acquirePlanningLock prend le verrou consultatif global du planning dans la
// transaction. Voir [planningLockID] pour le pourquoi d'un verrou unique.
func acquirePlanningLock(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, planningLockID); err != nil {
		return fmt.Errorf("verrouillage du planning : %w", err)
	}
	return nil
}

// checkEtapeGraph rejoue, sur l'état verrouillé de la transaction, les deux
// vérifications que le contrat du port exige : chaque prérequis désigne une
// étape existante, et le graphe — l'étape écrite comprise — reste acyclique.
// C'est l'adapter qui importe le domaine, jamais l'inverse : la règle
// (planning.CheckAcyclic) n'existe qu'à un seul endroit.
func checkEtapeGraph(ctx context.Context, q pgQuerier, etape planning.Etape) error {
	etapes, err := listEtapes(ctx, q)
	if err != nil {
		return err
	}

	known := make(map[planning.ID]bool, len(etapes))
	graph := make([]planning.Etape, 0, len(etapes)+1)
	for _, existing := range etapes {
		if existing.ID == etape.ID {
			continue
		}
		known[existing.ID] = true
		graph = append(graph, existing)
	}
	graph = append(graph, etape)

	for _, dep := range etape.DependsOn {
		if !known[dep] {
			return fmt.Errorf("%w : %s", planning.ErrUnknownDependency, dep)
		}
	}

	return planning.CheckAcyclic(graph)
}

// insertDependencies insère les prérequis d'une étape dans la transaction.
func insertDependencies(ctx context.Context, tx pgx.Tx, etape planning.Etape) error {
	if len(etape.DependsOn) == 0 {
		return nil
	}

	id, err := writeUUID(etape.ID.String(), "étape")
	if err != nil {
		return err
	}

	for _, dep := range etape.DependsOn {
		prerequis, err := writeUUID(dep.String(), "prérequis")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO etape_dependances (etape_id, prerequis_id) VALUES ($1, $2)`,
			id, prerequis); err != nil {
			return fmt.Errorf("insertion du prérequis %s de l'étape %s : %w", dep, etape.ID, err)
		}
	}

	return nil
}

// CreateJalon insère un jalon. Pas de verrou ici : un jalon n'appartient à
// aucun graphe, aucune écriture concurrente ne peut le rendre incohérent.
func (p *PlanningRepo) CreateJalon(ctx context.Context, jalon planning.Jalon) error {
	id, auteur, err := writeIDs(jalon.ID.String(), jalon.CreatedBy.String(), "jalon")
	if err != nil {
		return err
	}

	const insert = `
		INSERT INTO jalons (` + jalonColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := p.pool.Exec(ctx, insert,
		id, jalon.Name, jalon.Date, optionalTime(jalon.ReachedAt),
		auteur, jalon.CreatedAt, jalon.UpdatedAt); err != nil {
		return fmt.Errorf("insertion du jalon %s : %w", jalon.ID, err)
	}

	return nil
}

// JalonByID lit un jalon par son identifiant.
func (p *PlanningRepo) JalonByID(ctx context.Context, jalonID planning.ID) (planning.Jalon, error) {
	id, err := lookupUUID(jalonID.String(), planning.ErrUnknownJalon)
	if err != nil {
		return planning.Jalon{}, err
	}

	const query = `SELECT ` + jalonColumns + ` FROM jalons WHERE id = $1`

	jalon, err := scanJalon(p.pool.QueryRow(ctx, query, id))
	if err != nil {
		return planning.Jalon{}, fmt.Errorf("lecture du jalon %s : %w", jalonID, err)
	}

	return jalon, nil
}

// ListJalons renvoie tous les jalons, par date prévue puis identifiant.
func (p *PlanningRepo) ListJalons(ctx context.Context) ([]planning.Jalon, error) {
	jalons, err := queryAll(ctx, p.pool, scanJalon,
		`SELECT `+jalonColumns+` FROM jalons ORDER BY date_prevue, id`)
	if err != nil {
		return nil, fmt.Errorf("lecture des jalons : %w", err)
	}

	return jalons, nil
}

// UpdateJalon réécrit un jalon entier, sous la même garde optimiste que
// [PlanningRepo.UpdateEtape].
func (p *PlanningRepo) UpdateJalon(ctx context.Context, jalon planning.Jalon, expected time.Time) error {
	id, err := writeUUID(jalon.ID.String(), "jalon")
	if err != nil {
		return err
	}

	const update = `
		UPDATE jalons
		   SET nom = $2, date_prevue = $3, atteint_le = $4, modifie_le = $5
		 WHERE id = $1 AND modifie_le = $6`

	tag, err := p.pool.Exec(ctx, update,
		id, jalon.Name, jalon.Date, optionalTime(jalon.ReachedAt), jalon.UpdatedAt, expected)
	if err != nil {
		return fmt.Errorf("réécriture du jalon %s : %w", jalon.ID, err)
	}
	if tag.RowsAffected() == 0 {
		if _, readErr := p.JalonByID(ctx, jalon.ID); readErr != nil {
			return readErr
		}
		return fmt.Errorf("%w : jalon %s", planning.ErrConcurrentUpdate, jalon.ID)
	}

	return nil
}

// etapeDependency est une arête du graphe telle que la table la stocke.
type etapeDependency struct {
	etape     planning.ID
	prerequis planning.ID
}

// scanDependency reconstruit une arête depuis une ligne.
func scanDependency(row rowScanner) (etapeDependency, error) {
	var etapeID, prerequisID pgtype.UUID
	if err := row.Scan(&etapeID, &prerequisID); err != nil {
		return etapeDependency{}, err
	}

	return etapeDependency{
		etape:     planning.ID(etapeID.String()),
		prerequis: planning.ID(prerequisID.String()),
	}, nil
}

// scanName lit une colonne de nom seule.
func scanName(row rowScanner) (string, error) {
	var name string
	if err := row.Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

// scanEtape reconstruit une étape depuis une ligne — sans ses dépendances,
// qui vivent dans leur propre table.
func scanEtape(row rowScanner) (planning.Etape, error) {
	var (
		etape     planning.Etape
		id        pgtype.UUID
		debutReel pgtype.Timestamptz
		finReelle pgtype.Timestamptz
		auteur    pgtype.UUID
	)

	err := row.Scan(&id, &etape.Name, &etape.Description, &etape.PlannedStart, &etape.PlannedEnd,
		&debutReel, &finReelle, &etape.DevisID, &auteur, &etape.CreatedAt, &etape.UpdatedAt)
	if err != nil {
		return planning.Etape{}, scanError(err, planning.ErrUnknownEtape)
	}

	etape.ID = planning.ID(id.String())
	if debutReel.Valid {
		etape.ActualStart = debutReel.Time
	}
	if finReelle.Valid {
		etape.ActualEnd = finReelle.Time
	}
	etape.CreatedBy = planning.ActeurID(auteur.String())

	return etape, nil
}

// scanJalon reconstruit un jalon depuis une ligne.
func scanJalon(row rowScanner) (planning.Jalon, error) {
	var (
		jalon     planning.Jalon
		id        pgtype.UUID
		atteintLe pgtype.Timestamptz
		auteur    pgtype.UUID
	)

	err := row.Scan(&id, &jalon.Name, &jalon.Date, &atteintLe, &auteur, &jalon.CreatedAt, &jalon.UpdatedAt)
	if err != nil {
		return planning.Jalon{}, scanError(err, planning.ErrUnknownJalon)
	}

	jalon.ID = planning.ID(id.String())
	if atteintLe.Valid {
		jalon.ReachedAt = atteintLe.Time
	}
	jalon.CreatedBy = planning.ActeurID(auteur.String())

	return jalon, nil
}
