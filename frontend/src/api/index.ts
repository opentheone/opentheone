export interface ApiResult<T = unknown> {
  code: number;
  msg: string;
  data: T;
}

const TOKEN_KEY = "oto.token";

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || "";
}
export function setToken(t: string): void {
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
}

export async function api<T = unknown>(
  path: string,
  body?: Record<string, unknown>,
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  const tok = getToken();
  if (tok) headers["Authorization"] = `Bearer ${tok}`;

  const resp = await fetch(path, {
    method: "POST",
    headers,
    body: body ? JSON.stringify(body) : "{}",
  });

  if (resp.status === 401) {
    setToken("");
    if (!path.startsWith("/api/auth/")) {
      const next = window.location.pathname + window.location.search;
      if (!window.location.pathname.startsWith("/login")) {
        window.location.replace(`/login?next=${encodeURIComponent(next)}`);
      }
    }
    throw new Error("未登录或登录已过期");
  }

  let payload: ApiResult<T>;
  try {
    payload = (await resp.json()) as ApiResult<T>;
  } catch {
    throw new Error(`bad json from ${path} (status ${resp.status})`);
  }
  if (payload.code !== 0) {
    throw new Error(payload.msg || `code ${payload.code}`);
  }
  return payload.data;
}
