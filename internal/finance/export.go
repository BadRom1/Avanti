package finance

import (
	"io"
	"time"
)

// ExportFormat est le port de rendu du dossier d'assurance — et le second point
// d'extension officiel d'Avanti (docs/ARCHITECTURE.md §3, après
// document.Storage) : ajouter un format d'export consiste à implémenter ces
// trois méthodes dans adapters/export et à l'enregistrer dans cmd/avanti.
//
// Le domaine ne rend rien lui-même : il définit la forme du dossier
// ([DossierAssurance]) et laisse chaque implémentation décider de sa
// présentation — CSV pour le comptable, PDF pour l'assureur, demain autre
// chose.
type ExportFormat interface {
	// ContentType rend le type MIME du rendu.
	ContentType() string
	// FileExtension rend l'extension de fichier, sans point.
	FileExtension() string
	// Write rend le dossier d'assurance dans w.
	Write(w io.Writer, dossier DossierAssurance) error
}

// PieceJointe est une pièce du domaine document rattachée à une ligne du
// dossier, réduite à ce que l'export énumère : son nom et son classement.
//
// C'est une valeur passée par l'appelant : le domaine finance ne connaît pas le
// domaine document (R2), il transporte ce qu'on lui donne.
type PieceJointe struct {
	// FileName est le nom de fichier de la pièce.
	FileName string
	// Category est le classement de la pièce, tel que le domaine document le
	// stocke (facture, courrier_assurance…), transporté en simple chaîne.
	Category string
}

// LigneFacture est une facture mise sous la forme que le dossier présente.
//
// Tout y est valeur, assemblée par l'appelant : le libellé du devis vient du
// domaine devis, les pièces du domaine document — le domaine finance ne résout
// rien lui-même (R2), c'est l'adapter qui compose la vue transverse.
type LigneFacture struct {
	// DevisLibelle nomme le devis rattaché — lot et entreprise, au format de
	// l'appelant. Vide pour une dépense hors devis.
	DevisLibelle string
	// Entreprise est le nom de qui a facturé.
	Entreprise string
	// Numero est la référence de la facture, vide s'il n'y en a pas.
	Numero string
	// Date est la date que porte la facture.
	Date time.Time
	// Montant est le montant TTC, en centimes.
	Montant Montant
	// Paiement est l'état de règlement.
	Paiement StatutPaiement
	// PaidAt est la date du règlement, nulle pour une facture impayée.
	PaidAt time.Time
	// Assurance est le suivi d'indemnisation.
	Assurance SuiviAssurance
	// Pieces sont les pièces jointes rattachées à la facture.
	Pieces []PieceJointe
}

// LigneAcompte est un acompte mis sous la forme que le dossier présente.
type LigneAcompte struct {
	// DevisLibelle nomme le devis rattaché, vide pour un versement hors devis.
	DevisLibelle string
	// Entreprise est le nom de qui a été payé.
	Entreprise string
	// Date est la date du versement.
	Date time.Time
	// Montant est la somme versée, en centimes.
	Montant Montant
	// Moyen est le canal du versement.
	Moyen MoyenPaiement
	// Assurance est le suivi d'indemnisation.
	Assurance SuiviAssurance
	// Pieces sont les pièces jointes rattachées à l'acompte.
	Pieces []PieceJointe
}

// TotauxDossier sont les cumuls du chantier, en centimes.
type TotauxDossier struct {
	// Engage est le cumul des montants des devis retenus, fourni par
	// l'appelant : le domaine finance ne lit pas les devis.
	Engage Montant
	// Facture est le cumul des factures.
	Facture Montant
	// Paye est le cumul de ce qui est sorti : factures payées et acomptes
	// versés.
	Paye Montant
	// Rembourse est le cumul des indemnités reçues, factures et acomptes
	// confondus.
	Rembourse Montant
}

// DossierAssurance est le dossier rendu par un [ExportFormat] : l'état
// financier du chantier, pièce par pièce, avec ses justificatifs.
//
// C'est une structure de valeurs, entièrement assemblée par l'appelant —
// l'adapter web aujourd'hui, l'adapter MCP demain — qui interroge chaque
// domaine puis compose (R2 de docs/ARCHITECTURE.md). Le domaine finance en fixe
// la forme, pas le contenu.
type DossierAssurance struct {
	// GeneratedAt est l'instant de génération du dossier.
	GeneratedAt time.Time
	// Intitule nomme l'instance — le chantier — en tête du dossier.
	Intitule string
	// Factures sont les factures du chantier, avec leurs justificatifs.
	Factures []LigneFacture
	// Acomptes sont les versements du chantier.
	Acomptes []LigneAcompte
	// Totaux sont les cumuls du chantier.
	Totaux TotauxDossier
}
