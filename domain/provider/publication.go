package provider

import "context"

// PublicationJournal includes direct advancements without a refresh report.
// Acknowledgement follows confirmed cache publication and never repeats a CAS.
type PublicationJournal interface {
	Unpublished(context.Context) ([]Advancement, error)
	Acknowledge(context.Context, []string) error
}
