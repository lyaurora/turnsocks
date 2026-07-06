import { useEffect, useState } from "react";
import type { ThemeMode } from "../types/panel";

export function useTheme() {
  const [theme, setTheme] = useState<ThemeMode>(() => (localStorage.getItem("turnsocks-theme") as ThemeMode) || "system");

  useEffect(() => {
    const applyTheme = () => {
      const dark = theme === "dark" || (theme === "system" && window.matchMedia?.("(prefers-color-scheme: dark)").matches);
      document.documentElement.classList.toggle("dark", dark);
      localStorage.setItem("turnsocks-theme", theme);
    };
    applyTheme();
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    media?.addEventListener("change", applyTheme);
    return () => media?.removeEventListener("change", applyTheme);
  }, [theme]);

  return { theme, setTheme };
}
