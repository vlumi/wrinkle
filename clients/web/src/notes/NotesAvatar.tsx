/** The notes' avatar: a page glyph in a circle. */
export function NotesAvatar() {
  return (
    <span className="avatar notes" data-testid="notes-avatar" aria-hidden="true">
      <svg viewBox="0 0 24 24" width="55%" height="55%" fill="currentColor">
        <path d="M6 2h9l5 5v13a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2zm8 1.5V8h4.5z" />
        <path d="M7.5 12h9v1.5h-9zm0 3.5h9V17h-9z" fill="var(--accent)" />
      </svg>
    </span>
  );
}
