import axios from "@/api";

const getExchangeConfigAPI = () =>
  axios({
    url: "/api/exchange/config",
    method: "post",
    data: {}
  });

const saveExchangeConfigAPI = (data: any) =>
  axios({
    url: "/api/exchange/save",
    method: "post",
    data
  });

const testExchangeConfigAPI = (provider: "binance" | "okx") =>
  axios({
    url: "/api/exchange/test",
    method: "post",
    data: { provider }
  });

export { getExchangeConfigAPI, saveExchangeConfigAPI, testExchangeConfigAPI };
