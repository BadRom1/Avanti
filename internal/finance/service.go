package finance

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Repository est le port de persistance du domaine.
//
// Les implémentations sont attendues sur trois points que le domaine ne peut
// pas vérifier lui-même :
//
//   - rendre [ErrUnknownFacture] ou [ErrUnknownAcompte] (éventuellement
//     enveloppée) quand une lecture ne trouve rien — et quand une réécriture ne
//     touche aucune ligne ;
//   - garantir l'invariant central sur [Repository.CreateAcompte] : le cumul
//     des acomptes d'un devis ne dépasse pas montantEngage. La vérification que
//     [Service.RecordAcompte] fait avant d'écrire ne remplace pas celle-là,
//     elle sert seulement à répondre vite et clairement dans le cas courant.
//     L'implémentation doit **sérialiser** les insertions d'un même devisID —
//     l'adapter PostgreSQL le fait par un verrou consultatif transactionnel —
//     puis relire le cumul sous ce verrou : deux insertions simultanées au ras
//     de la limite ne peuvent pas passer toutes les deux. Le dépassement se
//     refuse avec [ErrAcomptesExceedEngagement]. Un acompte sans devisID
//     échappe à l'invariant : rien d'engagé à comparer, aucun verrou à prendre ;
//   - exécuter [Repository.UpdateFacture] et [Repository.UpdateAcompte] en
//     réécrivant la ligne entière, SOUS GARDE OPTIMISTE : la réécriture ne
//     touche la ligne que si son horodatage de modification vaut encore
//     expected — l'état que l'appelant a lu avant de transformer. Sans cette
//     garde, deux transitions simultanées (l'une paie, l'autre envoie à
//     l'assurance) se liraient l'une l'autre avant l'écriture, et la seconde
//     réécriture ferait régresser l'état posé par la première. Aucune ligne
//     touchée se départage en relisant : la pièce a disparu →
//     [ErrUnknownFacture] ou [ErrUnknownAcompte] ; elle existe →
//     [ErrConcurrentUpdate], et l'appelant relit avant de recommencer. La
//     base ne rejoue pas les transitions — ses contraintes CHECK gardent
//     seulement les états incohérents.
//
// Tout le reste des erreurs remonte tel quel et sera traité comme une panne.
type Repository interface {
	// CreateFacture insère une facture.
	CreateFacture(ctx context.Context, facture Facture) error
	// FactureByID lit une facture par son identifiant.
	FactureByID(ctx context.Context, id ID) (Facture, error)
	// ListFactures renvoie toutes les factures, de la plus récente à la plus
	// ancienne (date de la pièce, puis date de saisie).
	ListFactures(ctx context.Context) ([]Facture, error)
	// UpdateFacture réécrit une facture entière — transitions de paiement et
	// d'assurance comprises, modifie_le inclus — si la ligne porte encore
	// l'horodatage de modification expected (voir la garde optimiste du
	// contrat de port).
	UpdateFacture(ctx context.Context, facture Facture, expected time.Time) error

	// CreateAcompte insère un acompte, sous l'invariant du montant engagé
	// quand l'acompte porte un devisID (voir le contrat du port).
	CreateAcompte(ctx context.Context, acompte Acompte, montantEngage Montant) error
	// AcompteByID lit un acompte par son identifiant.
	AcompteByID(ctx context.Context, id ID) (Acompte, error)
	// ListAcomptes renvoie tous les acomptes, du plus récent au plus ancien.
	ListAcomptes(ctx context.Context) ([]Acompte, error)
	// UpdateAcompte réécrit un acompte entier, sous la même garde optimiste
	// que [Repository.UpdateFacture].
	UpdateAcompte(ctx context.Context, acompte Acompte, expected time.Time) error
	// SumAcomptesByDevis rend le cumul des acomptes rattachés à un devis. Un
	// devis sans acompte rend zéro, pas une erreur.
	SumAcomptesByDevis(ctx context.Context, devisID string) (Montant, error)
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
		return nil, errors.New("finance : dépôt manquant")
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

// FactureInput est ce qu'il faut fournir pour enregistrer une facture.
type FactureInput struct {
	// DevisID rattache la facture à un devis retenu, par identifiant faible.
	// Vide pour une dépense hors devis. C'est l'adapter appelant qui vérifie
	// que le devis existe et qu'il est retenu — le domaine ne sait pas le lire.
	DevisID string
	// Entreprise est le nom de qui a facturé. Obligatoire.
	Entreprise string
	// Montant est le montant TTC, en centimes. Strictement positif.
	Montant Montant
	// Date est la date que porte la facture. Obligatoire.
	Date time.Time
	// Numero est la référence de la facture. Facultatif.
	Numero string
	// Notes complètent la facture. Facultatives.
	Notes string
	// By est l'acteur qui saisit. Obligatoire.
	By ActeurID
}

// RecordFacture enregistre une facture et renvoie ce qui a été stocké. Une
// facture naît impayée et non envoyée à l'assurance.
func (s *Service) RecordFacture(ctx context.Context, in FactureInput) (Facture, error) {
	facture, err := s.buildFacture(in)
	if err != nil {
		return Facture{}, err
	}

	if writeErr := s.repo.CreateFacture(ctx, facture); writeErr != nil {
		return Facture{}, writeErr
	}

	return facture, nil
}

// buildFacture valide et assemble la facture. Séparer la construction de
// l'écriture garde chacune lisible, et rend la validation testable sans dépôt.
func (s *Service) buildFacture(in FactureInput) (Facture, error) {
	devisID, err := normalizeDevisID(in.DevisID)
	if err != nil {
		return Facture{}, err
	}
	entreprise, err := normalizeEntreprise(in.Entreprise)
	if err != nil {
		return Facture{}, err
	}
	numero, err := normalizeNumero(in.Numero)
	if err != nil {
		return Facture{}, err
	}
	notes, err := normalizeNotes(in.Notes)
	if err != nil {
		return Facture{}, err
	}
	if valuesErr := checkPieceValues(in.Montant, in.Date, in.By); valuesErr != nil {
		return Facture{}, valuesErr
	}

	id, err := s.newID()
	if err != nil {
		return Facture{}, err
	}

	now := s.clock().UTC()

	return Facture{
		ID:         id,
		DevisID:    devisID,
		Entreprise: entreprise,
		Montant:    in.Montant,
		Date:       in.Date.UTC(),
		Numero:     numero,
		Notes:      notes,
		Paiement:   PaiementImpayee,
		Assurance:  newSuiviAssurance(),
		RecordedBy: in.By,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// AcompteInput est ce qu'il faut fournir pour enregistrer un acompte versé.
type AcompteInput struct {
	// DevisID rattache l'acompte à un devis retenu, par identifiant faible.
	// Vide pour un versement hors devis.
	DevisID string
	// Entreprise est le nom de qui a été payé. Obligatoire.
	Entreprise string
	// Montant est la somme versée, en centimes. Strictement positive.
	Montant Montant
	// Date est la date du versement. Obligatoire.
	Date time.Time
	// Moyen est le canal du versement. Obligatoire.
	Moyen MoyenPaiement
	// Notes complètent le versement. Facultatives.
	Notes string
	// MontantEngage est le montant du devis retenu que l'acompte paie, EN
	// VALEUR : le domaine ne peut pas lire le devis lui-même (R1/R2), c'est
	// l'adapter appelant qui interroge le domaine devis et le transmet.
	// Obligatoire dès que DevisID est renseigné ; ignoré sinon — un acompte
	// hors devis échappe à l'invariant, rien d'engagé à comparer.
	MontantEngage Montant
	// By est l'acteur qui saisit. Obligatoire.
	By ActeurID
}

// RecordAcompte enregistre un acompte versé et renvoie ce qui a été stocké.
//
// L'invariant central du domaine s'applique ici : le cumul des acomptes d'un
// devis ne dépasse pas le montant engagé. Le service relit le cumul existant et
// refuse tôt, avec un message qui dit les trois montants — mais cette lecture
// ne garantit rien face à deux saisies simultanées : c'est
// [Repository.CreateAcompte] qui fait foi, en sérialisant les insertions d'un
// même devis et en revérifiant sous ce verrou. L'égalité est acceptée : solder
// exactement l'engagement est le cas nominal de fin de chantier.
func (s *Service) RecordAcompte(ctx context.Context, in AcompteInput) (Acompte, error) {
	acompte, err := s.buildAcompte(in)
	if err != nil {
		return Acompte{}, err
	}

	if acompte.DevisID != "" {
		if checkErr := s.checkEngagement(ctx, acompte, in.MontantEngage); checkErr != nil {
			return Acompte{}, checkErr
		}
	}

	if writeErr := s.repo.CreateAcompte(ctx, acompte, in.MontantEngage); writeErr != nil {
		return Acompte{}, writeErr
	}

	return acompte, nil
}

// checkEngagement vérifie tôt que l'acompte tient sous le montant engagé.
func (s *Service) checkEngagement(ctx context.Context, acompte Acompte, engage Montant) error {
	if !engage.Valid() {
		return fmt.Errorf("%w : devis %s", ErrMissingEngagement, acompte.DevisID)
	}

	existing, err := s.repo.SumAcomptesByDevis(ctx, acompte.DevisID)
	if err != nil {
		return err
	}
	if existing+acompte.Montant > engage {
		return fmt.Errorf("%w : %s déjà versés, %s demandés, %s engagés",
			ErrAcomptesExceedEngagement, existing, acompte.Montant, engage)
	}

	return nil
}

// buildAcompte valide et assemble l'acompte.
func (s *Service) buildAcompte(in AcompteInput) (Acompte, error) {
	devisID, err := normalizeDevisID(in.DevisID)
	if err != nil {
		return Acompte{}, err
	}
	entreprise, err := normalizeEntreprise(in.Entreprise)
	if err != nil {
		return Acompte{}, err
	}
	notes, err := normalizeNotes(in.Notes)
	if err != nil {
		return Acompte{}, err
	}
	if !in.Moyen.Known() {
		return Acompte{}, fmt.Errorf("%w : %q", ErrUnknownMoyenPaiement, in.Moyen)
	}
	if valuesErr := checkPieceValues(in.Montant, in.Date, in.By); valuesErr != nil {
		return Acompte{}, valuesErr
	}

	id, err := s.newID()
	if err != nil {
		return Acompte{}, err
	}

	now := s.clock().UTC()

	return Acompte{
		ID:         id,
		DevisID:    devisID,
		Entreprise: entreprise,
		Montant:    in.Montant,
		Date:       in.Date.UTC(),
		Moyen:      in.Moyen,
		Notes:      notes,
		Assurance:  newSuiviAssurance(),
		RecordedBy: in.By,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// checkPieceValues vérifie ce que factures et acomptes partagent : le montant,
// la date de la pièce, l'acteur.
func checkPieceValues(montant Montant, date time.Time, by ActeurID) error {
	switch {
	case !montant.Valid():
		return fmt.Errorf("%w : %s", ErrInvalidMontant, montant)
	case date.IsZero():
		return fmt.Errorf("%w : date de la pièce", ErrMissingDate)
	case by == "":
		return ErrMissingActor
	default:
		return nil
	}
}

// Factures renvoie toutes les factures, de la plus récente à la plus ancienne.
func (s *Service) Factures(ctx context.Context) ([]Facture, error) {
	return s.repo.ListFactures(ctx)
}

// Facture lit une facture par son identifiant.
func (s *Service) Facture(ctx context.Context, id ID) (Facture, error) {
	return s.repo.FactureByID(ctx, id)
}

// Acomptes renvoie tous les acomptes, du plus récent au plus ancien.
func (s *Service) Acomptes(ctx context.Context) ([]Acompte, error) {
	return s.repo.ListAcomptes(ctx)
}

// Acompte lit un acompte par son identifiant.
func (s *Service) Acompte(ctx context.Context, id ID) (Acompte, error) {
	return s.repo.AcompteByID(ctx, id)
}

// MarkFacturePayee marque une facture comme réglée.
func (s *Service) MarkFacturePayee(ctx context.Context, id ID, by ActeurID) (Facture, error) {
	return s.updateFacture(ctx, id, by, Facture.MarkPayee)
}

// MarkFactureEnvoyeeAssurance marque une facture comme transmise à l'assurance.
func (s *Service) MarkFactureEnvoyeeAssurance(ctx context.Context, id ID, by ActeurID) (Facture, error) {
	return s.updateFacture(ctx, id, by, Facture.MarkEnvoyeeAssurance)
}

// MarkFactureRemboursee marque une facture comme indemnisée du montant donné.
func (s *Service) MarkFactureRemboursee(ctx context.Context, id ID, rembourse Montant, by ActeurID) (Facture, error) {
	return s.updateFacture(ctx, id, by, func(facture Facture, at time.Time) (Facture, error) {
		return facture.MarkRemboursee(rembourse, at)
	})
}

// updateFacture relit la facture, joue la transition — c'est l'entité qui dit
// ce qu'elle autorise — puis réécrit la ligne entière, sous garde optimiste :
// l'horodatage lu (current.UpdatedAt) accompagne l'écriture, et une
// modification concurrente ressort en [ErrConcurrentUpdate] plutôt que
// d'être écrasée.
//
// L'acteur n'est pas conservé sur la ligne, seule la date de modification
// l'est ; il est exigé quand même, parce qu'aucune action du domaine n'est
// anonyme et que le refus doit venir d'ici, pas de chaque adapter.
func (s *Service) updateFacture(
	ctx context.Context, id ID, by ActeurID,
	transition func(Facture, time.Time) (Facture, error),
) (Facture, error) {
	if by == "" {
		return Facture{}, ErrMissingActor
	}

	current, err := s.repo.FactureByID(ctx, id)
	if err != nil {
		return Facture{}, err
	}

	updated, err := transition(current, s.clock().UTC())
	if err != nil {
		return Facture{}, err
	}

	if writeErr := s.repo.UpdateFacture(ctx, updated, current.UpdatedAt); writeErr != nil {
		return Facture{}, writeErr
	}

	return updated, nil
}

// MarkAcompteEnvoyeAssurance marque un acompte comme transmis à l'assurance.
func (s *Service) MarkAcompteEnvoyeAssurance(ctx context.Context, id ID, by ActeurID) (Acompte, error) {
	return s.updateAcompte(ctx, id, by, Acompte.MarkEnvoyeAssurance)
}

// MarkAcompteRembourse marque un acompte comme indemnisé du montant donné.
func (s *Service) MarkAcompteRembourse(ctx context.Context, id ID, rembourse Montant, by ActeurID) (Acompte, error) {
	return s.updateAcompte(ctx, id, by, func(acompte Acompte, at time.Time) (Acompte, error) {
		return acompte.MarkRembourse(rembourse, at)
	})
}

// updateAcompte est le pendant de [Service.updateFacture] pour les acomptes.
func (s *Service) updateAcompte(
	ctx context.Context, id ID, by ActeurID,
	transition func(Acompte, time.Time) (Acompte, error),
) (Acompte, error) {
	if by == "" {
		return Acompte{}, ErrMissingActor
	}

	current, err := s.repo.AcompteByID(ctx, id)
	if err != nil {
		return Acompte{}, err
	}

	updated, err := transition(current, s.clock().UTC())
	if err != nil {
		return Acompte{}, err
	}

	if writeErr := s.repo.UpdateAcompte(ctx, updated, current.UpdatedAt); writeErr != nil {
		return Acompte{}, writeErr
	}

	return updated, nil
}

// TotalFinance sont les cumuls d'un périmètre — un devis, le hors-devis, le
// chantier entier — en centimes.
type TotalFinance struct {
	// Facture est le cumul des factures.
	Facture Montant
	// Paye est le cumul de ce qui est sorti : factures payées et acomptes
	// versés. Une facture payée et l'acompte qui la règle sont deux pièces :
	// c'est à la saisie de ne pas doubler, pas à la synthèse de deviner.
	Paye Montant
	// Rembourse est le cumul des indemnités reçues, factures et acomptes
	// confondus.
	Rembourse Montant
}

// add cumule une pièce dans le total.
func (t TotalFinance) add(facture, paye, rembourse Montant) TotalFinance {
	t.Facture += facture
	t.Paye += paye
	t.Rembourse += rembourse
	return t
}

// Totaux sont les cumuls du chantier, groupés par devis.
type Totaux struct {
	// ParDevis donne le total de chaque devis référencé par au moins une pièce,
	// indexé par identifiant faible. Un devis sans pièce n'y figure pas :
	// l'appelant qui affiche tous les devis retenus complète avec des zéros.
	ParDevis map[string]TotalFinance
	// HorsDevis cumule les pièces sans rattachement.
	HorsDevis TotalFinance
	// Chantier cumule tout, rattaché ou non.
	Chantier TotalFinance
}

// Totaux rend les cumuls groupés par devis, en deux lectures quel que soit le
// nombre de devis : les factures puis les acomptes sont lus d'un bloc et
// répartis en mémoire — le modèle de devis.Comparaisons, une requête par devis
// ferait grandir les allers-retours au rythme de la synthèse affichée.
func (s *Service) Totaux(ctx context.Context) (Totaux, error) {
	factures, err := s.repo.ListFactures(ctx)
	if err != nil {
		return Totaux{}, err
	}

	acomptes, err := s.repo.ListAcomptes(ctx)
	if err != nil {
		return Totaux{}, err
	}

	return ComputeTotaux(factures, acomptes), nil
}

// ComputeTotaux répartit en cumuls des pièces DÉJÀ LUES, sans rien relire.
//
// C'est la même agrégation que [Service.Totaux], offerte à l'appelant qui tient
// déjà les factures et les acomptes — la page des finances et le dossier
// d'assurance les affichent ligne à ligne en plus de leurs totaux. Passer par
// le service leur coûterait deux lectures de plus pour le même résultat.
func ComputeTotaux(factures []Facture, acomptes []Acompte) Totaux {
	totaux := Totaux{ParDevis: make(map[string]TotalFinance)}
	for _, facture := range factures {
		paye := Montant(0)
		if facture.Paiement == PaiementPayee {
			paye = facture.Montant
		}
		totaux.cumulate(facture.DevisID, facture.Montant, paye, facture.Assurance.MontantRembourse)
	}
	for _, acompte := range acomptes {
		totaux.cumulate(acompte.DevisID, 0, acompte.Montant, acompte.Assurance.MontantRembourse)
	}

	return totaux
}

// cumulate ajoute une pièce au périmètre qui la porte et au total du chantier.
func (t *Totaux) cumulate(devisID string, facture, paye, rembourse Montant) {
	if devisID == "" {
		t.HorsDevis = t.HorsDevis.add(facture, paye, rembourse)
	} else {
		t.ParDevis[devisID] = t.ParDevis[devisID].add(facture, paye, rembourse)
	}
	t.Chantier = t.Chantier.add(facture, paye, rembourse)
}
