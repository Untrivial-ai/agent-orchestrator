package domain

// ChatProviderHandoff reserves an independent provider-history boundary for a
// verified Terminal -> Chat handoff. It does not assert native ancestry. Nil
// means ordinary resume, which must retain exact provider identity.
//
// Session Manager proves the handoff; Chat prepares the native replay; storage
// rechecks this snapshot when publishing history and controller ownership in one
// transaction. This is an internal reservation, never a client-supplied override.
type ChatProviderHandoff struct {
	BoundaryID              string
	ConversationID          string
	PreviousSessionID       SessionID
	PreviousBranchID        string
	PreviousSequence        int64
	ExpectedControllerOwner SessionControllerOwner
}
