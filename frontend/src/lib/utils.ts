import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** A random ID for client_request_id. */
export function randomId() {
  return crypto.randomUUID().replaceAll("-", "");
}
