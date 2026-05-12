import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import router from "./router";
import "./style.css";
import { useAuthStore } from "./store/auth";

const app = createApp(App);
app.use(createPinia());
app.use(router);

const auth = useAuthStore();
auth.bootstrap();

app.mount("#app");
