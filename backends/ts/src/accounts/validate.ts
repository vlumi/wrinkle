// The limits on what a user names and chooses.
import { invalidArgument } from "../internal/apierror.ts";
import { validLabel } from "../internal/label.ts";

const maxNameBytes = 64;
const maxDeviceNameBytes = 64;
const minPasswordBytes = 8;
const maxPasswordBytes = 256;

// The standard icons; empty is none.
const icons = ["cat", "dog", "fox", "owl", "bear", "frog", "panda", "rabbit", "tiger", "penguin", "octopus", "unicorn"];

/** Whether a user name is acceptable: 1 to 64 bytes of UTF-8, no control characters, no white space at either end. */
export const validName = (name: string): boolean => validLabel(name, maxNameBytes);

export function validPassword(password: string): boolean {
  const n = Buffer.byteLength(password, "utf8");
  return n >= minPasswordBytes && n <= maxPasswordBytes;
}

export const validIcon = (icon: string): boolean => icon === "" || icons.includes(icon);

export function checkName(name: string): void {
  if (!validName(name))
    throw invalidArgument(
      `name must be 1 to ${maxNameBytes} bytes of UTF-8, without control characters or white space at either end`,
    );
}

export function checkPassword(password: string): void {
  if (!validPassword(password))
    throw invalidArgument(`password must be ${minPasswordBytes} to ${maxPasswordBytes} bytes`);
}

export function checkDeviceName(name: string): void {
  if (Buffer.byteLength(name, "utf8") > maxDeviceNameBytes || !name.isWellFormed())
    throw invalidArgument(`device_name must be UTF-8 of at most ${maxDeviceNameBytes} bytes`);
}
