import { defineStore } from "pinia";
import { ref } from "vue";
import { api, getToken, setToken } from "@/api";

export interface User {
  id: string;
  username: string;
  display_name: string;
  role: string;
}

export const useAuthStore = defineStore("auth", () => {
  const user = ref<User | null>(null);
  const ready = ref(false);

  async function bootstrap() {
    if (!getToken()) {
      ready.value = true;
      return;
    }
    try {
      user.value = await api<User>("/api/auth/me");
    } catch {
      setToken("");
    } finally {
      ready.value = true;
    }
  }

  async function login(username: string, password: string) {
    const data = await api<{ token: string; user: User }>(
      "/api/auth/login",
      { username, password },
    );
    setToken(data.token);
    user.value = data.user;
  }

  async function register(
    username: string,
    password: string,
    displayName?: string,
  ) {
    const data = await api<{ token: string; user: User }>(
      "/api/auth/register",
      { username, password, display_name: displayName || "" },
    );
    setToken(data.token);
    user.value = data.user;
  }

  function logout() {
    setToken("");
    user.value = null;
  }

  return { user, ready, bootstrap, login, register, logout };
});
