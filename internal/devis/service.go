package devis

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Repository est le port de persistance du domaine.
//
// Les implémentations sont attendues sur trois points que le domaine ne peut pas
// vérifier lui-même :
//
//   - rendre [ErrUnknownDemande] ou [ErrUnknownDevis] (éventuellement
//     enveloppée) quand une lecture ne trouve rien ;
//   - rendre [ErrDevisAlreadyDecided] quand [Repository.Retain] ou
//     [Repository.Reject] ne trouve plus le devis au statut « recu » ;
//   - exécuter [Repository.Retain] de façon **indivisible**. Le devis retenu et
//     le refus de ses concurrents forment une seule décision : une base qui
//     laisserait passer la moitié de l'opération produirait une demande sans
//     devis retenu mais avec des concurrents refusés, c'est-à-dire une
//     comparaison qu'on ne peut plus ni lire ni reprendre ;
//   - rendre [ErrDemandeClosed] depuis [Repository.CreateDevis] quand la
//     demande porte déjà un devis retenu, et **sérialiser** cette insertion
//     avec [Repository.Retain]. Les deux ne peuvent pas se croiser : ou le
//     devis arrive avant la décision, qui le refuse alors avec les autres
//     concurrents, ou il arrive après et l'insertion est refusée. La
//     vérification que le service fait avant d'écrire ne remplace pas celle-là,
//     elle sert seulement à répondre vite et clairement dans le cas courant.
//
// Tout le reste des erreurs remonte tel quel et sera traité comme une panne.
type Repository interface {
	// CreateDemande insère une demande.
	CreateDemande(ctx context.Context, demande DemandeDevis) error
	// DemandeByID lit une demande par son identifiant.
	DemandeByID(ctx context.Context, id ID) (DemandeDevis, error)
	// ListDemandes renvoie toutes les demandes, de la plus récemment envoyée à
	// la plus ancienne.
	ListDemandes(ctx context.Context) ([]DemandeDevis, error)

	// CreateDevis insère un devis reçu, sauf si la demande est close.
	CreateDevis(ctx context.Context, devis Devis) error
	// DevisByID lit un devis par son identifiant.
	DevisByID(ctx context.Context, id ID) (Devis, error)
	// ListDevisByDemande renvoie les devis d'une demande.
	ListDevisByDemande(ctx context.Context, demandeID ID) ([]Devis, error)
	// ListDevis renvoie tous les devis, toutes demandes confondues.
	ListDevis(ctx context.Context) ([]Devis, error)

	// Retain retient un devis et refuse, dans la même opération indivisible, les
	// devis encore « recu » de la même demande.
	Retain(ctx context.Context, devisID ID, by ActeurID, at time.Time) error
	// Reject refuse un devis sans rien retenir.
	Reject(ctx context.Context, devisID ID, by ActeurID, at time.Time) error
}

// Service porte les cas d'usage du domaine.
//
// Il ne journalise pas et ne lit aucune variable d'environnement : ce qu'il lui
// faut arrive par [ServiceOptions], conformément à R1 de docs/ARCHITECTURE.md.
type Service struct {
	repo  Repository
	clock func() time.Time
	newID func() (ID, error)
}

// ServiceOptions rassemble les dépendances du service.
type ServiceOptions struct {
	// Repo est le port de persistance. Obligatoire.
	Repo Repository
	// Clock donne l'heure courante. Nil signifie time.Now.
	Clock func() time.Time
	// NewID tire un identifiant. Nil signifie [NewID].
	NewID func() (ID, error)
}

// NewService construit le service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Repo == nil {
		return nil, errors.New("devis : dépôt manquant")
	}

	service := &Service{repo: opts.Repo, clock: opts.Clock, newID: opts.NewID}
	if service.clock == nil {
		service.clock = time.Now
	}
	if service.newID == nil {
		service.newID = NewID
	}

	return service, nil
}

// DemandeInput est ce qu'il faut fournir pour ouvrir une consultation.
type DemandeInput struct {
	// Lot est l'intitulé du lot de travaux. Obligatoire.
	Lot string
	// Description précise ce qui est demandé. Facultative.
	Description string
	// Artisans sont les entreprises sollicitées. La liste peut être vide.
	Artisans []Artisan
	// SentAt est la date d'envoi de la consultation. Obligatoire.
	SentAt time.Time
	// By est l'acteur qui ouvre la demande. Obligatoire.
	By ActeurID
}

// CreateDemande ouvre une consultation et renvoie ce qui a été stocké.
func (s *Service) CreateDemande(ctx context.Context, in DemandeInput) (DemandeDevis, error) {
	demande, err := s.buildDemande(in)
	if err != nil {
		return DemandeDevis{}, err
	}

	if writeErr := s.repo.CreateDemande(ctx, demande); writeErr != nil {
		return DemandeDevis{}, writeErr
	}

	return demande, nil
}

// buildDemande valide et assemble la demande. Séparer la construction de
// l'écriture garde chacune lisible, et rend la validation testable sans dépôt.
func (s *Service) buildDemande(in DemandeInput) (DemandeDevis, error) {
	lot, err := NormalizeLot(in.Lot)
	if err != nil {
		return DemandeDevis{}, err
	}
	description, err := normalizeText(in.Description, maxDescriptionLength, "description")
	if err != nil {
		return DemandeDevis{}, err
	}
	artisans, err := NormalizeArtisans(in.Artisans)
	if err != nil {
		return DemandeDevis{}, err
	}
	if in.SentAt.IsZero() {
		return DemandeDevis{}, fmt.Errorf("%w : date d'envoi de la demande", ErrMissingDate)
	}
	if in.By == "" {
		return DemandeDevis{}, ErrMissingActor
	}

	id, err := s.newID()
	if err != nil {
		return DemandeDevis{}, err
	}

	now := s.clock().UTC()

	return DemandeDevis{
		ID:          id,
		Lot:         lot,
		Description: description,
		Artisans:    artisans,
		SentAt:      in.SentAt.UTC(),
		CreatedBy:   in.By,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// DevisInput est ce qu'il faut fournir pour enregistrer un devis reçu.
type DevisInput struct {
	// DemandeID rattache le devis à sa consultation. Obligatoire.
	DemandeID ID
	// Artisan est l'entreprise qui a chiffré. Obligatoire.
	Artisan Artisan
	// Montant est le prix proposé, en centimes. Strictement positif.
	Montant Montant
	// ReceivedAt est la date de réception. Obligatoire.
	ReceivedAt time.Time
	// Validity est la durée de validité annoncée. Zéro vaut « non renseignée ».
	Validity time.Duration
	// Notes porte ce que le devis ne dit pas. Facultatives.
	Notes string
	// DocumentIDs désigne les pièces jointes, par identifiant faible.
	DocumentIDs []string
	// By est l'acteur qui saisit le devis. Obligatoire.
	By ActeurID
}

// RecordDevis enregistre un devis reçu et renvoie ce qui a été stocké.
//
// La demande est relue d'abord, pour deux vérifications qui ne peuvent pas se
// faire depuis le devis seul : qu'elle existe, et qu'elle soit encore ouverte.
// Une demande dont un devis est retenu n'en accepte plus : la comparaison est
// close, et y ajouter une offre laisserait croire qu'elle est encore en jeu.
//
// Ces vérifications servent à refuser tôt et avec un message utile ; elles ne
// garantissent rien. Entre la lecture et l'écriture, une rétention concurrente
// peut clore la demande : c'est [Repository.CreateDevis] qui doit alors rendre
// [ErrDemandeClosed], et la base qui doit le lui dire.
func (s *Service) RecordDevis(ctx context.Context, in DevisInput) (Devis, error) {
	if in.DemandeID == "" {
		return Devis{}, ErrMissingDemande
	}

	if _, err := s.repo.DemandeByID(ctx, in.DemandeID); err != nil {
		return Devis{}, err
	}

	existing, err := s.repo.ListDevisByDemande(ctx, in.DemandeID)
	if err != nil {
		return Devis{}, err
	}
	if _, closed := retenu(existing); closed {
		return Devis{}, fmt.Errorf("%w : %s", ErrDemandeClosed, in.DemandeID)
	}

	devis, err := s.buildDevis(in)
	if err != nil {
		return Devis{}, err
	}

	if err := s.repo.CreateDevis(ctx, devis); err != nil {
		return Devis{}, err
	}

	return devis, nil
}

// buildDevis valide et assemble le devis reçu.
func (s *Service) buildDevis(in DevisInput) (Devis, error) {
	artisan, err := NormalizeArtisan(in.Artisan)
	if err != nil {
		return Devis{}, err
	}
	notes, err := normalizeText(in.Notes, maxDescriptionLength, "notes")
	if err != nil {
		return Devis{}, err
	}
	if valuesErr := checkDevisValues(in); valuesErr != nil {
		return Devis{}, valuesErr
	}

	id, err := s.newID()
	if err != nil {
		return Devis{}, err
	}

	now := s.clock().UTC()

	return Devis{
		ID:          id,
		DemandeID:   in.DemandeID,
		Artisan:     artisan,
		Montant:     in.Montant,
		ReceivedAt:  in.ReceivedAt.UTC(),
		Validity:    in.Validity,
		Notes:       notes,
		Statut:      StatutRecu,
		DocumentIDs: normalizeDocumentIDs(in.DocumentIDs),
		RecordedBy:  in.By,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// checkDevisValues vérifie ce qui n'est ni un texte ni un artisan : le montant,
// les dates, la validité, l'acteur.
func checkDevisValues(in DevisInput) error {
	switch {
	case !in.Montant.Valid():
		return fmt.Errorf("%w : %s", ErrInvalidMontant, in.Montant)
	case in.ReceivedAt.IsZero():
		return fmt.Errorf("%w : date de réception du devis", ErrMissingDate)
	case in.Validity < 0:
		return fmt.Errorf("%w : %s", ErrNegativeValidity, in.Validity)
	case in.By == "":
		return ErrMissingActor
	default:
		return nil
	}
}

// normalizeDocumentIDs nettoie les références de pièces jointes : blancs
// retirés, entrées vides et doublons écartés.
//
// Le domaine ne vérifie pas que ces identifiants désignent quelque chose : il ne
// connaît pas le domaine document, et c'est exactement ce que R2 de
// docs/ARCHITECTURE.md demande. Une pièce supprimée laisse donc une référence
// morte, que l'interface traitera comme telle.
func normalizeDocumentIDs(raw []string) []string {
	ids := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))

	for _, candidate := range raw {
		id := strings.TrimSpace(candidate)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}

		ids = append(ids, id)
	}

	if len(ids) == 0 {
		return nil
	}

	return ids
}

// Demandes renvoie toutes les consultations.
func (s *Service) Demandes(ctx context.Context) ([]DemandeDevis, error) {
	return s.repo.ListDemandes(ctx)
}

// Demande lit une consultation.
func (s *Service) Demande(ctx context.Context, id ID) (DemandeDevis, error) {
	return s.repo.DemandeByID(ctx, id)
}

// Devis lit un devis.
func (s *Service) Devis(ctx context.Context, id ID) (Devis, error) {
	return s.repo.DevisByID(ctx, id)
}

// AllDevis renvoie tous les devis, toutes consultations confondues.
func (s *Service) AllDevis(ctx context.Context) ([]Devis, error) {
	return s.repo.ListDevis(ctx)
}

// Compare rassemble une demande et ses devis, prêts à être mis en regard.
func (s *Service) Compare(ctx context.Context, demandeID ID) (Comparaison, error) {
	demande, err := s.repo.DemandeByID(ctx, demandeID)
	if err != nil {
		return Comparaison{}, err
	}

	propositions, err := s.repo.ListDevisByDemande(ctx, demandeID)
	if err != nil {
		return Comparaison{}, err
	}

	return newComparaison(demande, propositions), nil
}

// Comparaisons rend toutes les demandes avec leurs devis.
//
// Deux lectures suffisent, quel que soit le nombre de demandes : les devis sont
// lus d'un bloc puis répartis en mémoire. Une lecture par demande donnerait le
// même résultat en faisant grandir le nombre d'allers-retours avec la base au
// rythme de la liste affichée.
func (s *Service) Comparaisons(ctx context.Context) ([]Comparaison, error) {
	demandes, err := s.repo.ListDemandes(ctx)
	if err != nil {
		return nil, err
	}

	propositions, err := s.repo.ListDevis(ctx)
	if err != nil {
		return nil, err
	}

	byDemande := make(map[ID][]Devis, len(demandes))
	for _, devis := range propositions {
		byDemande[devis.DemandeID] = append(byDemande[devis.DemandeID], devis)
	}

	comparaisons := make([]Comparaison, 0, len(demandes))
	for _, demande := range demandes {
		comparaisons = append(comparaisons, newComparaison(demande, byDemande[demande.ID]))
	}

	return comparaisons, nil
}

// Retain retient un devis, ce qui refuse par ricochet les devis concurrents
// encore reçus de la même demande.
//
// C'est la décision qui clôt la comparaison, et elle est indivisible : le
// [Repository] écrit le devis retenu et les refus ensemble ou pas du tout. Les
// vérifications faites ici — le devis existe, il attend encore une décision,
// aucun concurrent n'est déjà retenu — servent à refuser tôt et avec un message
// utile ; c'est la base qui garantit qu'une seconde décision simultanée ne
// passera pas.
func (s *Service) Retain(ctx context.Context, devisID ID, by ActeurID) (Devis, error) {
	current, err := s.checkDecision(ctx, devisID, by, StatutRetenu)
	if err != nil {
		return Devis{}, err
	}

	if err := s.repo.Retain(ctx, current.ID, by, s.clock().UTC()); err != nil {
		return Devis{}, err
	}

	return s.repo.DevisByID(ctx, current.ID)
}

// Reject refuse un devis sans rien retenir. Les autres devis de la demande ne
// bougent pas : écarter une offre n'est pas en choisir une.
func (s *Service) Reject(ctx context.Context, devisID ID, by ActeurID) (Devis, error) {
	current, err := s.checkDecision(ctx, devisID, by, StatutRefuse)
	if err != nil {
		return Devis{}, err
	}

	if err := s.repo.Reject(ctx, current.ID, by, s.clock().UTC()); err != nil {
		return Devis{}, err
	}

	return s.repo.DevisByID(ctx, current.ID)
}

// checkDecision relit le devis et vérifie que la décision demandée est permise,
// pour le devis lui-même comme pour la demande qui le porte.
func (s *Service) checkDecision(ctx context.Context, devisID ID, by ActeurID, target Statut) (Devis, error) {
	if by == "" {
		return Devis{}, ErrMissingActor
	}

	current, err := s.repo.DevisByID(ctx, devisID)
	if err != nil {
		return Devis{}, err
	}

	// La transition est jouée à blanc : c'est l'entité qui dit ce qu'elle
	// autorise, et la rejouer ici la garderait d'accord avec elle par
	// imitation plutôt que par construction.
	if _, transitionErr := current.decide(target, by, s.clock().UTC()); transitionErr != nil {
		return Devis{}, transitionErr
	}

	siblings, err := s.repo.ListDevisByDemande(ctx, current.DemandeID)
	if err != nil {
		return Devis{}, err
	}
	if winner, closed := retenu(siblings); closed && winner.ID != current.ID {
		return Devis{}, fmt.Errorf("%w : %s", ErrDemandeClosed, current.DemandeID)
	}

	return current, nil
}

// Comparaison est une demande et ses devis mis en regard, triés du moins-disant
// au plus-disant.
//
// C'est la vue qui sert à décider, et le tri par montant est ce qui la rend
// utile : deux devis se lisent l'un sous l'autre, pas l'un après l'autre dans
// l'ordre où ils sont arrivés.
type Comparaison struct {
	// Demande est la consultation comparée.
	Demande DemandeDevis
	// Devis sont les propositions reçues, du montant le plus bas au plus haut.
	Devis []Devis
}

// newComparaison assemble la vue et fixe l'ordre de lecture.
func newComparaison(demande DemandeDevis, propositions []Devis) Comparaison {
	sorted := slices.Clone(propositions)

	// À montant égal — deux artisans qui s'alignent, ou un devis saisi deux fois
	// — la date de réception puis l'identifiant départagent. Sans ce second
	// critère, l'ordre d'affichage changerait d'une requête à l'autre.
	slices.SortFunc(sorted, func(a, b Devis) int {
		if a.Montant != b.Montant {
			return cmp.Compare(a.Montant, b.Montant)
		}
		if !a.ReceivedAt.Equal(b.ReceivedAt) {
			return a.ReceivedAt.Compare(b.ReceivedAt)
		}
		return strings.Compare(string(a.ID), string(b.ID))
	})

	return Comparaison{Demande: demande, Devis: sorted}
}

// Retenu rend le devis retenu de la demande, et faux tant qu'aucun ne l'est.
func (c Comparaison) Retenu() (Devis, bool) {
	return retenu(c.Devis)
}

// Closed dit que la consultation est tranchée, donc qu'elle n'accepte plus de
// nouveau devis.
func (c Comparaison) Closed() bool {
	_, closed := retenu(c.Devis)
	return closed
}

// MoinsDisant rend l'offre la moins chère, retenue ou non, et faux si aucun
// devis n'est encore arrivé.
//
// Le moins-disant n'est pas nécessairement le bon choix — c'est même la raison
// d'être de la comparaison — mais c'est le repère par rapport auquel tout écart
// se lit.
func (c Comparaison) MoinsDisant() (Devis, bool) {
	if len(c.Devis) == 0 {
		return Devis{}, false
	}
	return c.Devis[0], true
}

// Ecart rend la différence entre l'offre la plus chère et la moins chère. Elle
// vaut zéro tant qu'il n'y a pas deux devis à comparer.
func (c Comparaison) Ecart() Montant {
	if len(c.Devis) < 2 {
		return 0
	}
	return c.Devis[len(c.Devis)-1].Montant - c.Devis[0].Montant
}

// retenu cherche le devis retenu dans une liste.
func retenu(propositions []Devis) (Devis, bool) {
	index := slices.IndexFunc(propositions, func(candidate Devis) bool {
		return candidate.Statut == StatutRetenu
	})
	if index < 0 {
		return Devis{}, false
	}

	return propositions[index], true
}
