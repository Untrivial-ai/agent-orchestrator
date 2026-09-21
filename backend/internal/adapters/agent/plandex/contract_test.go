package plandex

import (
	"errors"
	"testing"
)

func TestObservedContractFailsClosed(t *testing.T) {
	err := ValidateContract(ObservedContract())
	for _, want := range []error{
		errInitialTaskContractMissing,
		errStandingInstructionsMissing,
		errObserverHooksMissing,
		errNativeSessionIdentityMissing,
		errExactRestoreMissing,
		errPermissionContractMissing,
		errBoundedCancellationMissing,
		errAuthenticationStatusMissing,
	} {
		if !errors.Is(err, want) {
			t.Fatalf("ValidateContract(ObservedContract()) error = %v, want errors.Is(_, %v)", err, want)
		}
	}
}

func TestValidateContractAcceptsProvenCapabilities(t *testing.T) {
	err := ValidateContract(Contract{
		Version:                    ObservedVersion,
		RaceFreeInitialTask:        true,
		HiddenStandingInstructions: true,
		IsolatedObserverHooks:      true,
		NativeSessionID:            true,
		ExactRestoreByID:           true,
		TruthfulPermissions:        true,
		BoundedCancellation:        true,
		AuthenticationStatus:       true,
	})
	if err != nil {
		t.Fatalf("ValidateContract() error = %v, want nil", err)
	}
}

func TestValidateContractRejectsReplacementHome(t *testing.T) {
	contract := Contract{
		Version:                    ObservedVersion,
		RaceFreeInitialTask:        true,
		HiddenStandingInstructions: true,
		IsolatedObserverHooks:      true,
		NativeSessionID:            true,
		ExactRestoreByID:           true,
		TruthfulPermissions:        true,
		BoundedCancellation:        true,
		AuthenticationStatus:       true,
		UsesReplacementHome:        true,
	}
	if err := ValidateContract(contract); !errors.Is(err, errReplacementProfileIsolationUnsafe) {
		t.Fatalf("ValidateContract() error = %v, want ErrReplacementProfileIsolationUnsafe", err)
	}
}

func TestValidateContractRejectsOldOrMalformedVersions(t *testing.T) {
	complete := Contract{
		RaceFreeInitialTask:        true,
		HiddenStandingInstructions: true,
		IsolatedObserverHooks:      true,
		NativeSessionID:            true,
		ExactRestoreByID:           true,
		TruthfulPermissions:        true,
		BoundedCancellation:        true,
		AuthenticationStatus:       true,
	}

	t.Run("old", func(t *testing.T) {
		contract := complete
		contract.Version = "2.2.0"
		if err := ValidateContract(contract); !errors.Is(err, errVersionTooOld) {
			t.Fatalf("ValidateContract() error = %v, want ErrVersionTooOld", err)
		}
	})

	t.Run("release metadata", func(t *testing.T) {
		contract := complete
		contract.Version = "v2.2.1+1"
		if err := ValidateContract(contract); err != nil {
			t.Fatalf("ValidateContract() error = %v, want nil", err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		contract := complete
		contract.Version = "development"
		if err := ValidateContract(contract); err == nil {
			t.Fatal("ValidateContract() error = nil, want parse error")
		}
	})
}
