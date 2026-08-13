package identity

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// UserRepository est le port de persistance des comptes.
//
// Les implémentations sont attendues sur deux points que le domaine ne peut pas
// vérifier lui-même : rendre [ErrUnknownUser] (éventuellement enveloppée)
// quand la lecture ne trouve rien, et [ErrEmailTaken] quand l'unicité de
// l'email est violée. Tout le reste des erreurs remonte tel quel et sera traité
// comme une panne.
type UserRepository interface {
	// Create insère un compte.
	Create(ctx context.Context, user User) error
	// ByEmail lit un compte par son email, déjà normalisé par l'appelant.
	ByEmail(ctx context.Context, email string) (User, error)
	// ByID lit un compte par son identifiant.
	ByID(ctx context.Context, id ID) (User, error)
	// Update réécrit les champs modifiables d'un compte : nom d'affichage,
	// empreinte, rôle, activité et date de modification. L'email et la date de
	// création ne changent pas.
	Update(ctx context.Context, user User) error
	// List renvoie tous les comptes, triés par email.
	List(ctx context.Context) ([]User, error)
}

// AccountService porte les cas d'usage de l'identité.
//
// Il ne journalise pas et ne lit aucune variable d'environnement : ce qu'il lui
// faut arrive par [ServiceOptions], conformément à R1 de docs/ARCHITECTURE.md.
type AccountService struct {
	repo   UserRepository
	hasher Hasher
	clock  func() time.Time
	newID  func() (ID, error)

	// decoy est une empreinte de secret aléatoire, calculée une fois à la
	// construction. Elle sert à faire payer à une tentative de connexion sur un
	// email inconnu exactement le même travail qu'une tentative sur un email
	// existant. Voir [AccountService.Authenticate].
	decoy PasswordHash
}

// ServiceOptions rassemble les dépendances du service.
type ServiceOptions struct {
	// Repo est le port de persistance. Obligatoire.
	Repo UserRepository
	// Hasher est le port de hachage. Obligatoire.
	Hasher Hasher
	// Clock donne l'heure courante. Nil signifie time.Now.
	Clock func() time.Time
	// NewID tire un identifiant de compte. Nil signifie [NewID].
	NewID func() (ID, error)
}

// NewAccountService construit le service.
//
// La construction calcule l'empreinte leurre, ce qui coûte un hachage complet —
// quelques dizaines de millisecondes avec [Argon2idHasher]. C'est le prix d'une
// authentification dont la durée ne dépend pas de l'existence du compte, et il
// est payé une fois au démarrage plutôt qu'à la première tentative de connexion,
// où il se lirait comme un écart de temps.
func NewAccountService(opts ServiceOptions) (*AccountService, error) {
	switch {
	case opts.Repo == nil:
		return nil, errors.New("identity : dépôt de comptes manquant")
	case opts.Hasher == nil:
		return nil, errors.New("identity : hacheur manquant")
	}

	service := &AccountService{
		repo:   opts.Repo,
		hasher: opts.Hasher,
		clock:  opts.Clock,
		newID:  opts.NewID,
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	if service.newID == nil {
		service.newID = NewID
	}

	secret, err := randomSecret(32)
	if err != nil {
		return nil, err
	}
	decoy, err := service.hasher.Hash(secret)
	if err != nil {
		return nil, fmt.Errorf("préparation de l'empreinte leurre : %w", err)
	}
	service.decoy = decoy

	return service, nil
}

// Create ouvre un compte et renvoie ce qui a été stocké.
//
// L'email et le nom d'affichage sont normalisés, le mot de passe est vérifié
// puis haché. Les validations passent toutes avant le hachage : refuser un rôle
// inconnu ne doit pas coûter un argon2id.
func (s *AccountService) Create(ctx context.Context, email, displayName, password string, role Role) (User, error) {
	normalizedEmail, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	name, err := NormalizeDisplayName(displayName)
	if err != nil {
		return User{}, err
	}
	if !role.Known() {
		return User{}, fmt.Errorf("%w : %q", ErrUnknownRole, role)
	}
	if policyErr := CheckPassword(password); policyErr != nil {
		return User{}, policyErr
	}

	id, err := s.newID()
	if err != nil {
		return User{}, err
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return User{}, err
	}

	now := s.clock().UTC()
	user := User{
		ID:           id,
		Email:        normalizedEmail,
		DisplayName:  name,
		PasswordHash: hash,
		Role:         role,
		Active:       true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.repo.Create(ctx, user); err != nil {
		return User{}, err
	}

	return user, nil
}

// Authenticate vérifie un couple email / mot de passe et renvoie l'acteur
// correspondant.
//
// Deux propriétés sont recherchées, et elles expliquent la forme du code :
//
//  1. la réponse ne distingue pas « email inconnu » de « mauvais mot de passe ».
//     Les deux rendent [ErrInvalidCredentials], sans quoi le formulaire de
//     connexion deviendrait un service d'énumération des comptes ;
//  2. la *durée* ne les distingue pas non plus. Un email inconnu suit exactement
//     le même chemin, en vérifiant le mot de passe contre l'empreinte leurre. Ce
//     n'est pas un appel factice ajouté à côté : c'est le même appel, sur une
//     empreinte que personne ne connaît.
//
// Un compte désactivé, lui, rend [ErrAccountDisabled] — mais seulement après que
// le mot de passe a été reconnu, ce qui n'apprend rien à qui ne le connaît pas.
func (s *AccountService) Authenticate(ctx context.Context, email, password string) (Actor, error) {
	user, err := s.forAuthentication(ctx, email)
	if err != nil {
		return Actor{}, err
	}

	matches, err := s.hasher.Verify(user.PasswordHash, password)
	if err != nil {
		return Actor{}, err
	}

	// user.ID vide signale le compte introuvable : la vérification a bien eu lieu,
	// contre le leurre, et son résultat ne peut pas valoir acceptation.
	if !matches || user.ID == "" {
		return Actor{}, ErrInvalidCredentials
	}
	if !user.Active {
		return Actor{}, ErrAccountDisabled
	}

	return user.Actor(), nil
}

// forAuthentication cherche le compte à vérifier et, à défaut, rend un compte
// vide porteur de l'empreinte leurre.
//
// C'est là que se joue l'égalité des temps : le seul chemin qui renvoie une
// erreur est celui d'une panne réelle du dépôt. Un email malformé comme un email
// inconnu ressortent ici avec du travail de hachage à faire, comme un email
// existant.
func (s *AccountService) forAuthentication(ctx context.Context, email string) (User, error) {
	// Le compte de repli porte l'empreinte leurre. C'est la valeur que rendent
	// tous les chemins qui ne trouvent pas de compte, email malformé compris :
	// la vérification d'empreinte a lieu dans tous les cas, donc le travail
	// accompli ne dépend pas de l'existence de l'adresse.
	unknown := User{PasswordHash: s.decoy}

	normalizedEmail, formatErr := NormalizeEmail(email)
	if formatErr == nil {
		user, readErr := s.repo.ByEmail(ctx, normalizedEmail)
		switch {
		case readErr == nil:
			return user, nil
		case !errors.Is(readErr, ErrUnknownUser):
			// Seule une panne réelle du dépôt ressort par ici. Elle ne doit surtout
			// pas se déguiser en refus d'identifiants : une base injoignable est un
			// incident à signaler, pas une faute de frappe de l'utilisateur.
			return User{}, readErr
		}
	}

	return unknown, nil
}

// ChangePassword remplace le mot de passe d'un compte après vérification de
// l'ancien.
//
// L'ancien mot de passe est exigé même quand la personne est déjà connectée :
// c'est ce qui empêche une session laissée ouverte sur un poste partagé de servir
// à confisquer le compte.
func (s *AccountService) ChangePassword(ctx context.Context, id ID, oldPassword, newPassword string) error {
	user, err := s.repo.ByID(ctx, id)
	if err != nil {
		return err
	}

	matches, err := s.hasher.Verify(user.PasswordHash, oldPassword)
	if err != nil {
		return err
	}
	if !matches {
		return ErrInvalidCredentials
	}

	if policyErr := CheckPassword(newPassword); policyErr != nil {
		return policyErr
	}

	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}

	user.PasswordHash = hash
	user.UpdatedAt = s.clock().UTC()

	return s.repo.Update(ctx, user)
}

// Deactivate ferme un compte sans le supprimer.
//
// La suppression n'est pas offerte : les actions qu'un compte a signées dans les
// autres domaines continuent de le désigner par son identifiant, et un
// identifiant qui ne résout plus rien transforme un historique en énigme.
// L'opération est idempotente — désactiver un compte déjà désactivé ne change
// rien et ne se plaint pas.
func (s *AccountService) Deactivate(ctx context.Context, id ID) error {
	user, err := s.repo.ByID(ctx, id)
	if err != nil {
		return err
	}
	if !user.Active {
		return nil
	}

	user.Active = false
	user.UpdatedAt = s.clock().UTC()

	return s.repo.Update(ctx, user)
}

// Reactivate rouvre un compte désactivé. C'est le pendant de
// [AccountService.Deactivate], et il est tout aussi idempotent.
func (s *AccountService) Reactivate(ctx context.Context, id ID) error {
	user, err := s.repo.ByID(ctx, id)
	if err != nil {
		return err
	}
	if user.Active {
		return nil
	}

	user.Active = true
	user.UpdatedAt = s.clock().UTC()

	return s.repo.Update(ctx, user)
}

// List renvoie tous les comptes, triés par email.
func (s *AccountService) List(ctx context.Context) ([]User, error) {
	return s.repo.List(ctx)
}

// ByID lit un compte. C'est ce qu'appelle l'intergiciel d'authentification web
// à chaque requête pour reconstruire l'acteur depuis la session : le compte est
// relu, donc une désactivation prend effet sans attendre l'expiration des
// sessions en cours.
func (s *AccountService) ByID(ctx context.Context, id ID) (User, error) {
	return s.repo.ByID(ctx, id)
}

// ByEmail lit un compte par son adresse, qu'elle soit normalisée ou non.
//
// C'est la façon de désigner un compte depuis la ligne de commande : un email se
// retape, un UUID se recopie. Cette méthode n'est *pas* celle du chemin
// d'authentification — [AccountService.Authenticate] a le sien, qui égalise les
// temps de réponse, et rien ne doit court-circuiter cela.
func (s *AccountService) ByEmail(ctx context.Context, email string) (User, error) {
	normalizedEmail, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	return s.repo.ByEmail(ctx, normalizedEmail)
}
