import {
	AVAudioSessionCategory,
	AVAudioSessionCategoryOptions,
	AVAudioSessionMode,
	ExpoSpeechRecognitionModule,
} from "expo-speech-recognition";
import { Platform } from "react-native";
import type { VoiceCallbacks, VoiceMode, VoiceProvider } from "./types";

// On-device speech recognition: iOS SFSpeechRecognizer, Android SpeechRecognizer.
// Chosen over a cloud recogniser because it is free, private, needs no API key
// and carries no per-minute cost — dictation is the kind of thing people leave
// on, and a metered one would be quietly expensive.

// Bias the recogniser toward words that show up constantly in agent prompts and
// would otherwise come back mangled ("get" for "git", "MPM" for "npm"). iOS maps
// this to SFSpeechRecognitionRequest.contextualStrings, Android to
// EXTRA_BIASING_STRINGS. Same trick Claude Code applies to its own dictation.
const CODING_VOCABULARY = [
	"git",
	"npm",
	"repo",
	"commit",
	"rebase",
	"branch",
	"merge",
	"PR",
	"diff",
	"refactor",
	"TypeScript",
	"JavaScript",
	"Go",
	"React",
	"Expo",
	"JSON",
	"API",
	"CLI",
	"daemon",
	"worktree",
	"regex",
	"localhost",
	"stack trace",
	"lint",
	"typecheck",
];

// How long stop() waits for the recogniser's `end` event before delivering the
// transcript anyway. Generous: `end` normally arrives in well under a second, and
// firing early would cut off a final result still in flight.
const STOP_GRACE_MS = 4000;

// Recognition accuracy drops sharply when the locale doesn't match the speaker's
// accent - "en-US" applied to an en-IN or en-GB speaker mishears steadily rather
// than failing outright, which reads as "it just transcribes the wrong words".
// So resolve the device's own locale against what the recogniser actually
// supports, and fall back to the same language in another region before giving up
// on en-US.
let resolvedLang: string | null = null;

const normalizeLocale = (s: string) => s.replace(/_/g, "-").toLowerCase();

async function resolveLang(): Promise<string> {
	if (resolvedLang) return resolvedLang;

	let device = "en-US";
	try {
		device = Intl.DateTimeFormat().resolvedOptions().locale || "en-US";
	} catch {
		// No Intl (unexpected on Hermes) - keep the default.
	}

	try {
		const { locales } = await ExpoSpeechRecognitionModule.getSupportedLocales({});
		const want = normalizeLocale(device);
		const exact = locales.find((l) => normalizeLocale(l) === want);
		if (exact) return (resolvedLang = exact);

		// e.g. device is en-IN but only en-GB is installed - still far closer than en-US.
		const language = want.split("-")[0];
		const sameLanguage = locales.find((l) => normalizeLocale(l).split("-")[0] === language);
		if (sameLanguage) return (resolvedLang = sameLanguage);
	} catch {
		// Locale enumeration is best-effort; en-US is the safe fallback.
	}

	return (resolvedLang = "en-US");
}

// LAST-RESORT segment detection, for a recognition service that emits neither a
// final result nor `speechstart` at a boundary. The real boundaries come from
// those two events; this only catches a partial that has obviously rolled over
// without one.
//
// Within a segment, successive partials restate the whole segment: they grow, and
// revise words in place ("add a test" -> "add a test for" -> "add attest for").
// They therefore keep sharing an opening. A partial that shares none belongs to a
// new segment, so the previous one is finished and must be banked.
function isSameSegment(prev: string, next: string): boolean {
	if (!prev.trim()) return true;
	const a = prev.trim().toLowerCase();
	const b = next.trim().toLowerCase();
	// Plain growth, or a revision that shortened the text.
	if (b.startsWith(a) || a.startsWith(b)) return true;
	// A revision can rewrite words the recogniser already emitted ("a test" ->
	// "attest"), so a character prefix is too brittle. But it still opens on the
	// same word and does not get shorter. A new segment after a pause starts fresh
	// and short, so this separates them without being fooled by re-transcription.
	const firstWord = (s: string) => s.slice(0, s.indexOf(" ") === -1 ? s.length : s.indexOf(" "));
	return firstWord(a) === firstWord(b) && b.length >= a.length;
}

// A "no speech" error is what you get from a mis-tap or a silent room. It is the
// expected outcome of a stray press, not a fault worth showing a banner for.
const SILENT_ERRORS = new Set(["no-speech", "aborted", "speech-timeout"]);

const ERROR_MESSAGES: Record<string, string> = {
	"not-allowed": "Microphone or speech permission was denied. Enable it in Settings to dictate.",
	"service-not-allowed": "Speech recognition is unavailable on this device.",
	"language-not-supported": "Speech recognition is not available for this language.",
	network: "Speech recognition needs a network connection and could not reach it.",
	"audio-capture": "Couldn't capture audio from the microphone.",
	interrupted: "Recording was interrupted.",
	busy: "The speech recogniser is busy. Try again in a moment.",
};

// Process-wide singleton. The provider caches the permission grant, and the
// native module caches the SFSpeechRecognizer across starts — both of which are
// wasted if a new provider appears every time a terminal screen mounts. Recording
// state lives inside one provider, and only one recording can run at a time
// anyway, so sharing is safe.
let shared: VoiceProvider | null = null;

export function getDeviceVoiceProvider(): VoiceProvider {
	shared ??= createDeviceVoiceProvider();
	return shared;
}

export function createDeviceVoiceProvider(): VoiceProvider {
	// Kick locale resolution off now rather than on the first press: warm-up is
	// already the sore point, and this must not add to it. If it hasn't finished by
	// the time the user speaks, start() falls back to en-US for that one phrase.
	void resolveLang();

	// Everything about one recording lives in a single object, so ending a
	// recording is "drop the session" rather than "reset five variables". A stale
	// event that somehow outlives its subscriptions finds `session` null and is
	// ignored, and there is no window where half the state has been cleared.
	type Session = {
		cb: VoiceCallbacks;
		subscriptions: { remove(): void }[];
		// Continuous recognition is SEGMENTED on Android: each pause closes a segment
		// with its own `isFinal` result, and the next segment starts from scratch
		// rather than restating what came before. So finished segments must be kept
		// and joined - treating any single result as "the transcript" silently drops
		// everything the user said after their first pause.
		finalized: string[];
		// The in-flight segment, replaced as it firms up (partials restate the whole
		// current segment, they are not deltas).
		partial: string;
		// Set by stop(); fires only if `end` never arrives. See stop().
		settleTimer?: ReturnType<typeof setTimeout>;
	};
	let session: Session | null = null;
	// Sticky once granted. Permission can only be revoked from Settings, which
	// restarts the app, so caching it for the process lifetime is safe.
	let granted = false;

	// Everything heard so far: completed segments plus the one in progress.
	function transcriptOf(s: Session): string {
		return [...s.finalized, s.partial].filter(Boolean).join(" ").trim();
	}

	function close(): VoiceCallbacks | null {
		if (!session) return null;
		const { cb, subscriptions, settleTimer } = session;
		session = null;
		if (settleTimer) clearTimeout(settleTimer);
		for (const sub of subscriptions) sub.remove();
		return cb;
	}

	// Ends the recording exactly once. Driven by `end` — documented as always the
	// last event, including after errors — rather than by a final result, because
	// in continuous mode a final result means "segment done", not "recording done".
	// Always reports through onFinal; an empty string is a legitimate result
	// meaning "nothing was said", which the caller treats as a no-op, not an error.
	function settle() {
		if (!session) return;
		const text = transcriptOf(session);
		const cb = close();
		cb?.onFinal(text);
	}

	function fail(message: string) {
		const cb = close();
		cb?.onError(message);
	}

	// Drop listeners before aborting so the resulting "aborted" error event can't
	// settle a session the caller has already walked away from.
	function abortSession() {
		close();
		try {
			ExpoSpeechRecognitionModule.abort();
		} catch {
			// Nothing was running.
		}
	}

	return {
		id: "device",

		language() {
			return resolvedLang;
		},

		available() {
			try {
				return ExpoSpeechRecognitionModule.isRecognitionAvailable();
			} catch {
				// Module missing entirely (e.g. a build that skipped prebuild).
				return false;
			}
		},

		async requestPermission() {
			// Every press goes through here, and this sits directly in front of the
			// microphone starting — so once permission is known to be granted, skip the
			// native round-trip entirely. Re-asking each time delayed the start of
			// recording enough to swallow the first words of a short phrase.
			if (granted) return true;
			try {
				const existing = await ExpoSpeechRecognitionModule.getPermissionsAsync();
				if (existing.granted) {
					granted = true;
					return true;
				}
				const res = await ExpoSpeechRecognitionModule.requestPermissionsAsync();
				granted = res.granted;
				return res.granted;
			} catch {
				return false;
			}
		},

		async start(cb, opts) {
			const longForm = opts?.mode === "latched";
			// A previous recording that never settled must not leak its listeners.
			// When there is no live session the native side is already stopped (every
			// settle path follows a native end/error), so don't abort for nothing.
			if (session) abortSession();
			const current: Session = { cb, subscriptions: [], finalized: [], partial: "" };
			session = current;

			current.subscriptions.push(
				ExpoSpeechRecognitionModule.addListener("result", (e) => {
					if (session !== current) return;
					const transcript = e.results?.[0]?.transcript ?? "";

					// iOS is ONE cumulative stream, not a sequence of segments. A single
					// recognition task runs over the whole recording and every result —
					// `isFinal` ones included — restates everything said so far. Banking
					// those the way Android's segments are banked appends the entire
					// phrase again on each final, and then the next cumulative partial
					// adds it a third time, which is why a long dictation snowballed into
					// "How the How's the system system turning How is the system turning?".
					//
					// So iOS keeps its whole stream in `partial` and never banks on a
					// final. The one case that does bank is a task restart (iOS caps how
					// long a single recognition task may run): its first result starts
					// from scratch and shares no opening with what came before, so the
					// finished task has to be preserved or the earlier speech is lost.
					if (Platform.OS === "ios") {
						if (!isSameSegment(current.partial, transcript) && current.partial.trim()) {
							current.finalized.push(current.partial.trim());
						}
						current.partial = transcript;
						cb.onPartial(transcriptOf(current));
						return;
					}

					if (e.isFinal) {
						// A closed segment, NOT the end of the recording. Bank it and wait —
						// the user is still holding the key and may keep talking. The final
						// supersedes the partial it was built from, so the partial is
						// dropped rather than banked alongside it.
						if (transcript.trim()) current.finalized.push(transcript.trim());
						current.partial = "";
					} else {
						// Normally `speechstart` has already banked the previous segment by
						// now. This only fires if the service announced no boundary at all.
						if (!isSameSegment(current.partial, transcript) && current.partial.trim()) {
							current.finalized.push(current.partial.trim());
						}
						current.partial = transcript;
					}
					cb.onPartial(transcriptOf(current));
				}),
			);

			current.subscriptions.push(
				// The authoritative segment boundary on Android, and the reason its
				// transcripts can now match iOS's.
				//
				// iOS runs ONE recognition task over the whole stream, so each result
				// restates everything said so far - pauses included. Android's
				// SpeechRecognizer is built for a single utterance, so it emits a
				// sequence of independent chunks that have to be stitched.
				// `onBeginningOfSpeech` fires at the start of each new utterance, so
				// anything still held as a partial belongs to the previous one.
				//
				// iOS is excluded deliberately: it fires `speechstart` once after a
				// result, and its partials are cumulative, so banking there would
				// duplicate the text rather than segment it.
				ExpoSpeechRecognitionModule.addListener("speechstart", () => {
					if (session !== current || Platform.OS !== "android") return;
					if (current.partial.trim()) {
						current.finalized.push(current.partial.trim());
						current.partial = "";
					}
				}),
			);

			current.subscriptions.push(
				ExpoSpeechRecognitionModule.addListener("start", () => {
					// The recogniser is live. Until this fires the microphone is still
					// warming up and speech is dropped on the floor, so this is the only
					// honest moment to prompt the user to talk.
					if (session === current) cb.onReady();
				}),
			);

			current.subscriptions.push(
				ExpoSpeechRecognitionModule.addListener("error", (e) => {
					if (session !== current) return;
					// Silence, a stray tap, or a deliberate abort is not a fault. Settle
					// normally so anything already transcribed survives.
					if (SILENT_ERRORS.has(e.error)) settle();
					else fail(ERROR_MESSAGES[e.error] ?? e.message ?? "Speech recognition failed.");
				}),
			);

			current.subscriptions.push(
				ExpoSpeechRecognitionModule.addListener("end", () => {
					// `end` is the only reliable "the recording is over" signal: it is
					// documented as always the last event dispatched, including after an
					// error, and unlike a final result it never also means "segment
					// boundary". This is what settles a normal recording.
					if (session === current) settle();
				}),
			);

			try {
				ExpoSpeechRecognitionModule.start({
					// Synchronous read of the pre-resolved value - awaiting here would add
					// to the warm-up the user is already waiting through.
					lang: resolvedLang ?? "en-US",
					interimResults: true,
					// Push-to-talk: the user's finger decides when the phrase ends, not a
					// silence timer. Unsupported on Android 12 and below, where it falls
					// back to auto-stop — still correct, just less forgiving of a pause.
					continuous: true,
					addsPunctuation: true,
					contextualStrings: CODING_VOCABULARY,
					// The audio session is the warm-up: measured at ~1.1s, dominated by
					// Bluetooth HFP route negotiation and configuring an output path.
					// That cost is intolerable before a three-second phrase and irrelevant
					// before a sixty-second one, so the two modes buy different sessions.
					// It cannot be changed mid-recording - iOS would need a teardown and
					// re-activation - which is why the mode is fixed at start.
					iosCategory: longForm
						? {
								// Hands-free dictation: worth the slower activation to let a
								// Bluetooth headset mic carry a long prompt.
								category: AVAudioSessionCategory.playAndRecord,
								categoryOptions: [
									AVAudioSessionCategoryOptions.allowBluetooth,
									AVAudioSessionCategoryOptions.defaultToSpeaker,
								],
								mode: AVAudioSessionMode.measurement,
							}
						: {
								// Push-to-talk: cheapest session that can capture. Built-in mic
								// only, no route negotiation, no output path.
								category: AVAudioSessionCategory.record,
								categoryOptions: [],
								mode: AVAudioSessionMode.default,
							},
					androidIntentOptions: {
						// Thinking mid-sentence is normal when dictating a prompt. Android's
						// default silence timeout is short enough to end the session while
						// the user is still holding the key, so widen it and let the finger
						// decide instead.
						EXTRA_SPEECH_INPUT_COMPLETE_SILENCE_LENGTH_MILLIS: 10000,
						EXTRA_SPEECH_INPUT_POSSIBLY_COMPLETE_SILENCE_LENGTH_MILLIS: 10000,
					},
				});
			} catch (e) {
				fail(e instanceof Error ? e.message : "Couldn't start the microphone.");
			}
		},

		stop() {
			const current = session;
			try {
				ExpoSpeechRecognitionModule.stop();
			} catch {
				// Already stopped; the `end` listener settles us.
			}
			if (!current) return;
			// Watchdog. `end` normally lands within a few hundred ms of stop() and is
			// what delivers the transcript. If the recogniser dies without emitting it
			// the UI would sit in "recording" forever with the words stranded — the
			// "it recorded but nothing happened" failure. Settle with what we heard.
			current.settleTimer = setTimeout(() => {
				if (session === current) settle();
			}, STOP_GRACE_MS);
		},

		abort: abortSession,
	};
}
