/** A group's avatar: a people glyph in a circle. */
export function GroupAvatar() {
  return (
    <span className="avatar group" data-testid="group-avatar" aria-hidden="true">
      <svg viewBox="0 0 24 24" width="60%" height="60%" fill="currentColor">
        <circle cx="9" cy="8" r="3.5" />
        <path d="M2 19c0-3.6 3.1-6 7-6s7 2.4 7 6v1H2z" />
        <circle cx="17" cy="9" r="2.8" opacity="0.7" />
        <path d="M17.5 13.2c2.6.3 4.5 2.2 4.5 5V20h-4.5v-1c0-2.2-.8-4.2-2.3-5.6.7-.2 1.5-.3 2.3-.2z" opacity="0.7" />
      </svg>
    </span>
  );
}
