package domain

// CIBillingBlockedReason is normalized provider evidence, stored in LogTail for
// unknown checks that could not execute. Read models expose only this known
// reason, never arbitrary job output, when presenting account intervention.
const CIBillingBlockedReason = "Job execution was blocked by account billing or spending limits."
