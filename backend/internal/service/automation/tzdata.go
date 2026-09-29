package automation

// Embed the IANA database so packaged desktop daemons do not depend on a Go
// installation or an operating-system zoneinfo directory.
import _ "time/tzdata" // Register the embedded timezone database with package time.
