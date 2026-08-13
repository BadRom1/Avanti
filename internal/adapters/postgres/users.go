package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// uniqueViolationCode est le SQLSTATE que PostgreSQL rend sur violation d'une
// contrainte d'unicité. C'est ce qui permet de traduire un conflit d'écriture en
// [identity.ErrEmailTaken] plutôt que de lire d'abord et d'écrire ensuite —
// une séquence qui laisserait la place à deux créations simultanées.
const uniqueViolationCode = "23505"

// uniqueEmailConstraint est le nom de l'index unique de la table users. Le tester
// nommément évite de traduire en « email déjà pris » la violation d'une *autre*
// contrainte d'unicité que la table gagnerait plus tard.
const uniqueEmailConstraint = "users_email_unique"

// UserRepo implémente [identity.UserRepository] sur PostgreSQL.
//
// Les colonnes de la table users portent le vocabulaire du domaine
// (nom_affichage, empreinte_mdp, actif, cree_le) : c'est le même modèle vu des
// deux côtés, et la correspondance se lit sans table de traduction.
type UserRepo struct {
	pool *pgxpool.Pool
}

// NewUserRepo construit le dépôt sur un pool de connexions existant. Le pool
// reste la propriété de l'appelant — cmd/avanti, qui l'ouvre et le ferme.
func NewUserRepo(pool *pgxpool.Pool) (*UserRepo, error) {
	if pool == nil {
		return nil, errors.New("postgres : pool de connexions manquant")
	}
	return &UserRepo{pool: pool}, nil
}

// userColumns est la liste de sélection commune aux trois lectures. La factoriser
// garantit que les colonnes arrivent dans l'ordre qu'attend [scanUser].
const userColumns = `id, email, nom_affichage, empreinte_mdp, role, actif, cree_le, modifie_le`

// Create insère un compte.
func (d *UserRepo) Create(ctx context.Context, user identity.User) error {
	id, err := toUUID(user.ID)
	if err != nil {
		return err
	}

	const query = `
		INSERT INTO users (` + userColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err = d.pool.Exec(ctx, query,
		id, user.Email, user.DisplayName, string(user.PasswordHash),
		string(user.Role), user.Active, user.CreatedAt, user.UpdatedAt)
	if err != nil {
		if isEmailTaken(err) {
			return fmt.Errorf("%w : %s", identity.ErrEmailTaken, user.Email)
		}
		return fmt.Errorf("insertion du compte %s : %w", user.Email, err)
	}

	return nil
}

// ByEmail lit un compte par son email, que l'appelant a déjà normalisé.
func (d *UserRepo) ByEmail(ctx context.Context, email string) (identity.User, error) {
	const query = `SELECT ` + userColumns + ` FROM users WHERE email = $1`

	user, err := scanUser(d.pool.QueryRow(ctx, query, email))
	if err != nil {
		return identity.User{}, fmt.Errorf("lecture du compte %s : %w", email, err)
	}

	return user, nil
}

// ByID lit un compte par son identifiant.
func (d *UserRepo) ByID(ctx context.Context, userID identity.ID) (identity.User, error) {
	id, err := toUUID(userID)
	if err != nil {
		return identity.User{}, err
	}

	const query = `SELECT ` + userColumns + ` FROM users WHERE id = $1`

	user, err := scanUser(d.pool.QueryRow(ctx, query, id))
	if err != nil {
		return identity.User{}, fmt.Errorf("lecture du compte %s : %w", userID, err)
	}

	return user, nil
}

// Update réécrit les champs modifiables d'un compte.
//
// L'email et la date de création sont délibérément absents du SET : l'un est
// l'identifiant de connexion, l'autre un fait historique. Les changer relèverait
// d'un autre cas d'usage, qui n'existe pas encore et devra être nommé.
func (d *UserRepo) Update(ctx context.Context, user identity.User) error {
	id, err := toUUID(user.ID)
	if err != nil {
		return err
	}

	const query = `
		UPDATE users
		   SET nom_affichage = $2,
		       empreinte_mdp = $3,
		       role          = $4,
		       actif         = $5,
		       modifie_le    = $6
		 WHERE id = $1`

	tag, err := d.pool.Exec(ctx, query,
		id, user.DisplayName, string(user.PasswordHash), string(user.Role), user.Active, user.UpdatedAt)
	if err != nil {
		return fmt.Errorf("mise à jour du compte %s : %w", user.ID, err)
	}
	// Zéro ligne touchée veut dire que le compte n'existe pas. Le signaler évite
	// qu'une mise à jour dans le vide passe pour un succès.
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mise à jour du compte %s : %w", user.ID, identity.ErrUnknownUser)
	}

	return nil
}

// List renvoie tous les comptes, triés par email.
//
// Sans pagination : la table compte deux ou trois lignes par nature. Le jour où
// ce ne serait plus vrai, c'est le port du domaine qu'il faudrait revoir, pas
// seulement cette requête.
func (d *UserRepo) List(ctx context.Context) ([]identity.User, error) {
	const query = `SELECT ` + userColumns + ` FROM users ORDER BY email`

	rows, err := d.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("lecture des comptes : %w", err)
	}
	defer rows.Close()

	var accounts []identity.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("lecture des comptes : %w", err)
		}
		accounts = append(accounts, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("parcours des comptes : %w", err)
	}

	return accounts, nil
}

// scanUser reconstruit un compte depuis une ligne.
//
// Le paramètre est l'intersection des interfaces de pgx.Row et pgx.Rows, ce qui
// permet de partager le décodage entre les lectures unitaires et le parcours de
// List — donc de n'avoir qu'un seul endroit où l'ordre des colonnes compte.
func scanUser(row interface{ Scan(dest ...any) error }) (identity.User, error) {
	var (
		user identity.User
		id   pgtype.UUID
		hash string
		role string
	)

	err := row.Scan(&id, &user.Email, &user.DisplayName, &hash, &role, &user.Active, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.User{}, identity.ErrUnknownUser
	}
	if err != nil {
		return identity.User{}, err
	}

	user.ID = identity.ID(id.String())
	user.PasswordHash = identity.PasswordHash(hash)
	user.Role = identity.Role(role)

	return user, nil
}

// toUUID traduit l'identifiant du domaine dans le type uuid de PostgreSQL.
//
// La conversion est explicite plutôt que laissée à pgx : le pilote encode le type
// uuid en binaire, où une chaîne Go n'a pas de plan d'encodage. La traduire ici
// a un second mérite — un identifiant mal formé est refusé avant d'atteindre le
// SQL, avec un message qui nomme la valeur fautive.
func toUUID(id identity.ID) (pgtype.UUID, error) {
	var uuid pgtype.UUID
	if err := uuid.Scan(string(id)); err != nil {
		return pgtype.UUID{}, fmt.Errorf("identifiant de compte %q illisible comme uuid : %w", id, err)
	}
	return uuid, nil
}

// isEmailTaken reconnaît la violation de l'index unique sur l'email.
func isEmailTaken(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == uniqueViolationCode && pgErr.ConstraintName == uniqueEmailConstraint
}
