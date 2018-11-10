import Vehicle from '../vehicle';
import Route from '../route';
import Stop from '../stop';
import Update from '../update';
import AdminMessageUpdate from '@/structures/adminMessageUpdate';
import routeScheduleInterval from '../routeScheduleInterval';
/**
 * Info service provider grabs the basic information from the api and returns it to the frontend.
 */
export default class InfoServiceProvider {
    public GrabVehicles(): Promise < Vehicle[] > {
        return fetch('/vehicles').then((data) => data.json()).then((data) => {
            const ret = new Array < Vehicle > ();
            data.forEach((element: {
                id: number,
                name: string,
                created: string,
                updated: string,
                enabled: boolean,
                tracker_id: string,
            }) => {
                ret.push(new Vehicle(element.id, element.name,
                    new Date(element.created), new Date(element.updated), element.enabled));
            });
            return ret;
        });

    }

    public GrabRoutes(): Promise < Route[] > {
        return fetch('/routes').then((data) => data.json()).then((data) => {
            const ret = new Array < Route > ();
            data.forEach((element: {
                id: number,
                name: string,
                description: string,
                enabled: boolean,
                color: string,
                width: number,
                points: [{
                    latitude: number,
                    longitude: number,
                }],
                schedule: [
                    {
                        id: number;
                        route_id: number;
                        start_day: number;
                        start_time: Date;
                        end_day: number;
                        end_time: Date;
                    }
                ],
            }) => {
                const myschedule: routeScheduleInterval[] = [];
                element.schedule.forEach((interval) => {
                    myschedule.push(new routeScheduleInterval(interval.id, interval.route_id, interval.start_day, new Date(interval.start_time), interval.end_day, new Date(interval.end_time)));
                });
                console.log(myschedule);
                ret.push(new Route(element.id, element.name, element.description,
                    element.enabled, element.color, Number(element.width), element.points, myschedule));
            });
            return ret;
        });
    }

    public GrabStops(): Promise < Stop[] > {
        return fetch('/stops').then((data) => data.json()).then((data) => {
            const ret = new Array < Stop > ();
            data.forEach((element: {
                id: number,
                name: string,
                description: string,
                latitude: string,
                longitude: string,
                created: string,
                updated: string,
            }) => {
                ret.push(new Stop(element.id, element.name, element.description, Number(element.latitude),
                    Number(element.longitude), element.created, element.updated));
            });
            return ret;
        });
    }

    public GrabAdminMessage(): Promise <AdminMessageUpdate> {
        return fetch('/adminMessage').then((data) => data.json()).then((ret) => {
            return new AdminMessageUpdate(ret.message, Boolean(ret.enabled), new Date(ret.created), new Date(ret.updated));
        }).catch(() => {
            return new AdminMessageUpdate('', false, new Date(), new Date());

        });
    }

    public GrabUpdates(): Promise < Update[] > {
        return fetch('/updates').then((data) => data.json()).then((data): Update[] => {
            const ret = new Array <Update> ();
            data.forEach((element: Update) => {
                ret.push(element);
            });
            return ret;
        });
    }
}
