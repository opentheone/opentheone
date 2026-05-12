import { createRouter, createWebHistory } from "vue-router";
import { useAuthStore } from "@/store/auth";

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: "/login",
      name: "login",
      component: () => import("@/views/LoginView.vue"),
      meta: { public: true },
    },
    {
      path: "/",
      component: () => import("@/views/AppShell.vue"),
      children: [
        {
          path: "",
          name: "dashboard",
          component: () => import("@/views/DashboardView.vue"),
        },
        {
          path: "personas",
          name: "personas",
          component: () => import("@/views/PersonasView.vue"),
        },
        {
          path: "personas/:id",
          name: "persona-detail",
          component: () => import("@/views/PersonaDetailView.vue"),
          props: true,
        },
        {
          path: "llm",
          name: "llm",
          component: () => import("@/views/LLMView.vue"),
        },
        {
          path: "conversations",
          name: "conversations",
          component: () => import("@/views/ConversationsView.vue"),
        },
        {
          path: "conversations/:id",
          name: "conversation-detail",
          component: () => import("@/views/ConversationDetailView.vue"),
          props: true,
        },
        {
          path: "settings",
          name: "settings",
          component: () => import("@/views/SettingsView.vue"),
        },
        {
          path: "admin",
          name: "admin",
          component: () => import("@/views/AdminView.vue"),
          meta: { adminOnly: true },
        },
      ],
    },
  ],
});

router.beforeEach(async (to) => {
  const auth = useAuthStore();
  if (!auth.ready) {
    await auth.bootstrap();
  }
  if (!to.meta.public && !auth.user) {
    return { name: "login", query: { redirect: to.fullPath } };
  }
  if (to.name === "login" && auth.user) {
    return { name: "dashboard" };
  }
  if (to.meta.adminOnly && auth.user?.role !== "admin") {
    return { name: "dashboard" };
  }
});

export default router;
