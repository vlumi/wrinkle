import { useState } from "react";
import type { ReactNode } from "react";
import { Avatar } from "../account/Avatar";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { hex, sameId } from "../ids";
import { useSession } from "../session/session";
import { pickable } from "./model";

const NONE: ReadonlySet<string> = new Set();

/** Contacts to check, with a filter by name: a group's members. */
export function ContactPicker({
  legend,
  selected,
  onChange,
  exclude = NONE,
}: {
  legend: string;
  selected: User[];
  onChange: (next: User[]) => void;
  /** Hex ids of users not to offer, such as current members. */
  exclude?: ReadonlySet<string>;
}) {
  const contacts = useSession((s) => s.contacts);
  const [filter, setFilter] = useState("");
  const all = pickable(contacts, "", exclude);
  const shown = pickable(contacts, filter, exclude);
  const isPicked = (u: User) => selected.some((s) => sameId(s.userId, u.userId));
  const toggle = (u: User) =>
    onChange(isPicked(u) ? selected.filter((s) => !sameId(s.userId, u.userId)) : [...selected, u]);

  let body: ReactNode;
  if (contacts === null) body = <p className="hint">Loading contacts…</p>;
  else if (all.length === 0) {
    body = (
      <p className="hint" data-testid="picker-empty">
        {exclude.size === 0 ? "Add contacts first: members are picked from them." : "No other contacts to add."}
      </p>
    );
  } else {
    body = (
      <>
        <input
          type="search"
          data-testid="contact-filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          onKeyDown={(e) => {
            // Enter filters; it does not submit the form.
            if (e.key === "Enter") e.preventDefault();
          }}
          placeholder="Filter contacts"
          aria-label="Filter contacts"
          autoCapitalize="none"
        />
        <ul className="picker-list" data-testid="picker">
          {shown.map((u) => (
            <li key={hex(u.userId)}>
              <label>
                <input
                  type="checkbox"
                  data-testid="pick"
                  data-name={u.name}
                  checked={isPicked(u)}
                  onChange={() => toggle(u)}
                />
                <Avatar user={u} />
                <span className="picker-name">{u.name}</span>
              </label>
            </li>
          ))}
          {shown.length === 0 && <li className="hint">No contact matches.</li>}
        </ul>
      </>
    );
  }
  return (
    <fieldset className="picker">
      <legend>
        {legend}
        {selected.length > 0 && <span className="picker-count"> · {selected.length} selected</span>}
      </legend>
      {body}
    </fieldset>
  );
}
