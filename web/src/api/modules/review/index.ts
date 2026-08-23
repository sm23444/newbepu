import axios from "@/api";

export const reviewListAPI = (data: any) => axios({ url: "/api/review/list", method: "post", data });
export const reviewDetailAPI = (data: any) => axios({ url: "/api/review/detail", method: "post", data });
export const reviewResolveAPI = (data: any) => axios({ url: "/api/review/resolve", method: "post", data });
