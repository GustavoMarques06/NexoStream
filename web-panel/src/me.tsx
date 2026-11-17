import { createContext, useContext } from "react";
import type { Principal } from "./types";

export const MeContext = createContext<Principal | null>(null);

export function useMe(): Principal | null {
  return useContext(MeContext);
}

export function useCan(scope: string): boolean {
  return useMe()?.scopes.includes(scope) ?? false;
}
